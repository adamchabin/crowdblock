package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	maxAPIKeysPerUser  = 10
	blacklistWindow    = time.Hour
	blacklistThreshold = 5
	apiKeyCacheTTL     = 60 * time.Second
	maxBodyBytes       = 4 << 10 // all request bodies are tiny JSON objects
)

// Set at build time: go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

var db *pgxpool.Pool
var rdb *redis.Client

func main() {
	log.Printf("crowdblock server %s", version)
	if debug {
		log.Printf("DEBUG logging enabled")
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL not set")
	}

	ctx := context.Background()

	if err := EnsureDatabase(ctx, dsn); err != nil {
		log.Fatalf("cannot prepare database: %v", err)
	}

	var err error
	db, err = pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("cannot connect to database: %v", err)
	}
	defer db.Close()

	if err := EnsureSchema(ctx, db); err != nil {
		log.Fatalf("cannot apply schema: %v", err)
	}

	if os.Getenv("SEED_DEV_DATA") == "1" {
		if err := EnsureSeed(ctx, db); err != nil {
			log.Fatalf("cannot load seed data: %v", err)
		}
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	rdb = redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("warning: cannot connect to redis at %s, API key auth will always hit postgres: %v", redisAddr, err)
	}
	defer rdb.Close()

	openGeoIP()

	go runListGenerator(ctx, listRefreshInterval())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleUI)
	mux.HandleFunc("GET /api/v1/stats", handleStats)
	mux.HandleFunc("POST /api/v1/register", handleRegister)
	mux.HandleFunc("POST /api/v1/api-keys", handleCreateAPIKey)
	mux.HandleFunc("GET /api/v1/api-keys", handleListAPIKeys)
	mux.HandleFunc("DELETE /api/v1/api-keys/{id}", handleRevokeAPIKey)
	mux.HandleFunc("DELETE /api/v1/account", handleDeleteAccount)
	mux.HandleFunc("POST /api/v1/reports", handleReportIP)
	mux.HandleFunc("GET /api/v1/ips", handleListIPs)

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("server listening on %s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

// logRequests logs every incoming HTTP request to the console: method, path,
// remote address, response status, bytes received / sent, and duration.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		body := &countingReader{ReadCloser: http.MaxBytesReader(w, r.Body, maxBodyBytes)}
		r.Body = body
		notes := &logNotes{}
		r = r.WithContext(context.WithValue(r.Context(), logNotesKey{}, notes))

		next.ServeHTTP(sw, r)

		log.Printf("%s %s %s %d in=%s out=%s %s%s", r.RemoteAddr, r.Method, r.URL.Path, sw.status,
			formatBytes(body.n), formatBytes(sw.n), time.Since(start), notes)
	})
}

// logNotes are details a handler adds to its request's log line, e.g.
// "list=full enc=gzip from=cache tier=1/1440".
type logNotes struct{ parts []string }

type logNotesKey struct{}

func (n *logNotes) String() string {
	if len(n.parts) == 0 {
		return ""
	}
	return " " + strings.Join(n.parts, " ")
}

// addLogNote appends "key=value" to the log line of the request.
func addLogNote(r *http.Request, key string, value any) {
	if n, ok := r.Context().Value(logNotesKey{}).(*logNotes); ok {
		n.parts = append(n.parts, fmt.Sprintf("%s=%v", key, value))
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	n      int64 // response body bytes
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}

func (sw *statusWriter) Write(b []byte) (int, error) {
	n, err := sw.ResponseWriter.Write(b)
	sw.n += int64(n)
	return n, err
}

// countingReader counts the request body bytes actually read (Content-Length
// is missing for chunked requests).
type countingReader struct {
	io.ReadCloser
	n int64
}

func (cr *countingReader) Read(b []byte) (int, error) {
	n, err := cr.ReadCloser.Read(b)
	cr.n += int64(n)
	return n, err
}

// formatBytes: 512 -> "512B", 363580 -> "355.1KiB", 5242880 -> "5.0MiB"
func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1024*1024))
	}
}

// ---------- REGISTRATION ----------

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"` // optional: when empty, a password is generated and e-mailed
}

// Per client address; behind a reverse proxy this is the proxy's address.
// shortcut: no per-email limit besides the cooldown, add one if abused.
const (
	registerPerHour  = 5
	registerCooldown = 10 * time.Minute // between mails to the same address
)

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		http.Error(w, "invalid data (email required)", http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		registerByEmail(w, r, req.Email)
		return
	}
	if len(req.Password) < 8 {
		http.Error(w, "invalid data (email + password min. 8 characters)", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	var userID string
	err = db.QueryRow(r.Context(),
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		req.Email, string(hash),
	).Scan(&userID)
	if err != nil {
		http.Error(w, "email already taken or database error", http.StatusConflict)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"user_id": userID})
}

// registerByEmail creates the account with a generated password and mails it.
// The answer is the same whether or not the address was already registered
// (an existing account is left untouched), so it can't be used to find out
// who has an account, and a stranger's address only ever gets one mail per
// registerCooldown.
func registerByEmail(w http.ResponseWriter, r *http.Request, rawEmail string) {
	if !smtpConfigured() {
		http.Error(w, "registration by e-mail is not available", http.StatusServiceUnavailable)
		return
	}
	email, ok := parseEmail(rawEmail)
	if !ok {
		http.Error(w, "invalid e-mail address", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ipKey := "register:ip:" + host
	n, err := rdb.Incr(ctx, ipKey).Result()
	if err != nil {
		log.Printf("redis INCR %s: %v", ipKey, err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if n == 1 {
		rdb.Expire(ctx, ipKey, time.Hour)
	}
	if n > registerPerHour {
		http.Error(w, "too many registrations, try again later", http.StatusTooManyRequests)
		return
	}

	// Same bcrypt cost for new and existing addresses: no timing difference.
	password, err := generatePassword()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	const reply = "If the address is valid, an e-mail with the password is on its way."
	mailKey := "register:mail:" + strings.ToLower(email)
	if fresh, err := rdb.SetNX(ctx, mailKey, 1, registerCooldown).Result(); err != nil || !fresh {
		writeJSON(w, http.StatusAccepted, map[string]string{"message": reply})
		return
	}

	var userID string
	err = db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 ON CONFLICT (email) DO NOTHING RETURNING id`, email, string(hash)).Scan(&userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows): // already registered: send nothing
	case err != nil:
		rdb.Del(ctx, mailKey)
		log.Printf("register %s: %v", email, err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	default:
		// Sent in the background so the response time doesn't depend on SMTP.
		// If the mail can't be sent the account is removed again, so the user
		// can register again instead of owning a password nobody received.
		go func() {
			if err := sendPasswordMail(email, password); err != nil {
				log.Printf("register: mail to %s failed: %v", email, err)
				bg := context.Background()
				_, _ = db.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
				rdb.Del(bg, mailKey)
			}
		}()
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": reply})
}

// ---------- API KEYS ----------
// Basic Auth (email/password) — key management only

type createKeyRequest struct {
	Name string `json:"name"`
}

func handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}

	var req createKeyRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // name is optional

	tx, err := db.Begin(r.Context())
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var activeCount int
	if err := tx.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM api_keys WHERE user_id = $1 AND revoked_at IS NULL`, userID,
	).Scan(&activeCount); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if activeCount >= maxAPIKeysPerUser {
		http.Error(w, "limit of 10 active keys reached", http.StatusForbidden)
		return
	}

	rawKey, keyHash, keyPrefix, err := generateAPIKey()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	var keyID string
	if err := tx.QueryRow(r.Context(),
		`INSERT INTO api_keys (user_id, key_prefix, key_hash, name) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, keyPrefix, keyHash, req.Name,
	).Scan(&keyID); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// full key is shown only once, at creation time
	writeJSON(w, http.StatusCreated, map[string]string{"id": keyID, "api_key": rawKey})
}

// dummyHash makes the login of a nonexistent user cost as much as a real
// one, so response time doesn't reveal which e-mails are registered.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.DefaultCost)

// authenticateUser checks Basic Auth (email/password) and returns the user's
// id. On failure it writes the 401 response and returns ok=false.
func authenticateUser(w http.ResponseWriter, r *http.Request) (userID string, ok bool) {
	email, password, hasAuth := r.BasicAuth()
	if !hasAuth {
		w.Header().Set("WWW-Authenticate", `Basic realm="api"`)
		http.Error(w, "authorization required", http.StatusUnauthorized)
		return "", false
	}

	passwordHash := string(dummyHash)
	err := db.QueryRow(r.Context(),
		`SELECT id, password_hash FROM users WHERE email = $1`, email,
	).Scan(&userID, &passwordHash)
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil || err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return "", false
	}
	return userID, true
}

// forgetAPIKeys drops the cached authentication of keys (by key_hash), so
// a revoked key stops working at once, not after apiKeyCacheTTL.
func forgetAPIKeys(ctx context.Context, hashes ...string) {
	keys := make([]string, len(hashes))
	for i, h := range hashes {
		keys[i] = "apikey:" + h
	}
	if len(keys) == 0 {
		return
	}
	if err := rdb.Del(ctx, keys...).Err(); err != nil {
		log.Printf("redis DEL error for %d api keys: %v", len(keys), err)
	}
}

// GET /api/v1/api-keys lists the user's active keys (ids, never the keys).
func handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}
	rows, err := db.Query(r.Context(),
		`SELECT id, key_prefix, COALESCE(name, ''), created_at FROM api_keys
		 WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at`, userID)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type keyInfo struct {
		ID        string    `json:"id"`
		Prefix    string    `json:"prefix"`
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"created_at"`
	}
	keys := []keyInfo{}
	for rows.Next() {
		var k keyInfo
		if err := rows.Scan(&k.ID, &k.Prefix, &k.Name, &k.CreatedAt); err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		keys = append(keys, k)
	}
	if rows.Err() != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// DELETE /api/v1/api-keys/{id} revokes one of the user's keys.
func handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}

	var keyHash string
	err := db.QueryRow(r.Context(),
		`UPDATE api_keys SET revoked_at = now()
		 WHERE id::text = $1 AND user_id = $2 AND revoked_at IS NULL RETURNING key_hash`,
		r.PathValue("id"), userID,
	).Scan(&keyHash)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "no such active key", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	forgetAPIKeys(r.Context(), keyHash)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/v1/account deletes the user with all keys and reports
// (ON DELETE CASCADE). Reports are gone from the lists at the next refresh;
// entries already in the blacklist table stay.
func handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}

	var hashes []string
	if err := db.QueryRow(r.Context(),
		`SELECT COALESCE(array_agg(key_hash), '{}') FROM api_keys WHERE user_id = $1`, userID,
	).Scan(&hashes); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// The user must also stop counting towards the blacklist threshold:
	// drop it from the reporter sets of the IPs it reported in the window.
	var ips []string
	if rows, err := db.Query(r.Context(),
		`SELECT DISTINCT host(ip) FROM ip_reports
		 WHERE user_id = $1 AND reported_at > now() - $2::int * interval '1 second'`,
		userID, int(blacklistWindow.Seconds())); err == nil {
		ips, _ = pgx.CollectRows(rows, pgx.RowTo[string])
	}

	if _, err := db.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, userID); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	if len(ips) > 0 {
		if _, err := rdb.Pipelined(r.Context(), func(p redis.Pipeliner) error {
			for _, ip := range ips {
				p.ZRem(r.Context(), reportersKey(ip), userID)
			}
			return nil
		}); err != nil {
			log.Printf("redis ZREM for deleted user %s: %v", userID, err)
		}
	}
	forgetAPIKeys(r.Context(), hashes...)
	w.WriteHeader(http.StatusNoContent)
}

func generateAPIKey() (raw, hash, prefix string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return
	}
	raw = "sfw_" + hex.EncodeToString(buf) // "sfw_" prefix makes the key easy to spot in logs etc.
	sum := sha256.Sum256([]byte(raw))
	hash = hex.EncodeToString(sum[:])
	prefix = raw[:8]
	return
}

// ---------- IP REPORTING ----------

type reportRequest struct {
	IP     string `json:"ip"`
	Source string `json:"source"` // optional: what detected the attack, e.g. "auth"
}

// Sources are short identifiers chosen by the reporter (plugin names).
var sourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func handleReportIP(w http.ResponseWriter, r *http.Request) {
	rawKey := r.Header.Get("X-API-Key")
	if rawKey == "" {
		http.Error(w, "missing X-API-Key header", http.StatusUnauthorized)
		return
	}

	apiKeyID, userID, err := authenticateAPIKey(r.Context(), rawKey)
	if err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		http.Error(w, "invalid data (ip field required)", http.StatusBadRequest)
		return
	}
	addr, err := netip.ParseAddr(req.IP)
	if err != nil || !isPublicIP(addr) {
		http.Error(w, "invalid data (ip must be a public IPv4/IPv6 address)", http.StatusBadRequest)
		return
	}
	req.IP = addr.Unmap().String()
	if req.Source != "" && !sourcePattern.MatchString(req.Source) {
		http.Error(w, "invalid data (source: up to 32 characters a-z, 0-9, _ and -)", http.StatusBadRequest)
		return
	}

	if _, err := db.Exec(r.Context(),
		`INSERT INTO ip_reports (ip, api_key_id, user_id, source) VALUES ($1, $2, $3, NULLIF($4, ''))`,
		req.IP, apiKeyID, userID, req.Source,
	); err != nil {
		http.Error(w, "invalid IP address or database error", http.StatusBadRequest)
		return
	}

	blacklisted, err := maybeBlacklist(r.Context(), req.IP, userID)
	if err != nil {
		log.Printf("blacklist aggregation error for %s: %v", req.IP, err)
	}

	debugf("report %s source=%q by user %s (key %s)", req.IP, req.Source, userID, apiKeyID)
	writeJSON(w, http.StatusCreated, map[string]any{"accepted": true, "blacklisted": blacklisted})
}

// isPublicIP rejects addresses that must never reach a blocklist: private,
// loopback, link-local, multicast, unspecified. (CIDR input fails ParseAddr.)
func isPublicIP(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.Zone() == "" && a.IsGlobalUnicast() && !a.IsPrivate()
}

// authenticateAPIKey resolves a raw API key to its (apiKeyID, userID) pair.
// Successful lookups are cached in Redis for apiKeyCacheTTL, so repeated
// requests from the same key don't hit postgres until the cache entry expires.
func authenticateAPIKey(ctx context.Context, rawKey string) (apiKeyID, userID string, err error) {
	sum := sha256.Sum256([]byte(rawKey))
	hash := hex.EncodeToString(sum[:])
	cacheKey := "apikey:" + hash

	if cached, cacheErr := rdb.Get(ctx, cacheKey).Result(); cacheErr == nil {
		if id, uid, ok := strings.Cut(cached, "|"); ok {
			debugf("api key %s: from cache", id)
			return id, uid, nil
		}
	} else if cacheErr != redis.Nil {
		log.Printf("redis GET error for %s: %v", cacheKey, cacheErr)
	}

	err = db.QueryRow(ctx,
		`SELECT id, user_id FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`, hash,
	).Scan(&apiKeyID, &userID)
	if err != nil {
		debugf("api key %s…: invalid or revoked", rawKey[:min(len(rawKey), 8)])
		return "", "", errors.New("key invalid or revoked")
	}
	debugf("api key %s: from database, cached for %s", apiKeyID, apiKeyCacheTTL)

	if setErr := rdb.Set(ctx, cacheKey, apiKeyID+"|"+userID, apiKeyCacheTTL).Err(); setErr != nil {
		log.Printf("redis SET error for %s: %v", cacheKey, setErr)
	}

	return
}

// reportersKey is the Redis sorted set of recent reporters of one IP.
func reportersKey(ip string) string { return "ip:reports:" + ip }

// maybeBlacklist records the report in a Redis sorted set (member = user_id,
// score = report time) keyed per IP, trims entries older than blacklistWindow,
// and counts the remainder to get the number of distinct recent reporters —
// this replaces the per-report COUNT(DISTINCT user_id) aggregate that used to
// run against postgres on every single call. If the threshold is reached, the
// (rare) blacklist upsert still goes to postgres for durable storage.
func maybeBlacklist(ctx context.Context, ip, userID string) (bool, error) {
	key := reportersKey(ip)
	now := time.Now()
	cutoff := now.Add(-blacklistWindow)

	if err := rdb.ZAdd(ctx, key, redis.Z{Score: float64(now.Unix()), Member: userID}).Err(); err != nil {
		return false, fmt.Errorf("redis ZADD: %w", err)
	}
	if err := rdb.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff.Unix(), 10)).Err(); err != nil {
		return false, fmt.Errorf("redis ZREMRANGEBYSCORE: %w", err)
	}
	if err := rdb.Expire(ctx, key, blacklistWindow).Err(); err != nil {
		return false, fmt.Errorf("redis EXPIRE: %w", err)
	}

	distinctReporters64, err := rdb.ZCard(ctx, key).Result()
	if err != nil {
		return false, fmt.Errorf("redis ZCARD: %w", err)
	}
	distinctReporters := int(distinctReporters64)

	if distinctReporters < blacklistThreshold {
		return false, nil
	}

	_, err = db.Exec(ctx, `
		INSERT INTO blacklist (ip, first_seen, last_seen, distinct_reporters)
		VALUES ($1, now(), now(), $2)
		ON CONFLICT (ip) DO UPDATE
		SET last_seen = now(), distinct_reporters = EXCLUDED.distinct_reporters`,
		ip, distinctReporters,
	)
	return err == nil, err
}

// ---------- IP LISTING ----------

type ipListEntry struct {
	IP                string   `json:"ip"`
	DistinctReporters int      `json:"distinct_reporters"`
	Country           string   `json:"country,omitempty"` // ISO 3166-1 alpha-2, e.g. "CN"
	Sources           []string `json:"sources,omitempty"` // e.g. ["auth", "fail2ban"], from the report window
}

// The list is served for fixed thresholds only, so that the server can
// precompute and cache every combination (5 × 4 lists).
var (
	allowedMinReporters = []int{1, 5, 10, 20, 50}
	allowedMinutes      = []int{60, 360, 1440, 10080} // 1 h, 6 h, 24 h, 7 days
)

// parseListParams validates min_reporters and minutes of GET /api/v1/ips.
func parseListParams(q url.Values) (minReporters, minutes int, err error) {
	minReporters, err = strconv.Atoi(q.Get("min_reporters"))
	if err != nil || !slices.Contains(allowedMinReporters, minReporters) {
		return 0, 0, fmt.Errorf("invalid data (min_reporters must be one of %v)", allowedMinReporters)
	}
	minutes, err = strconv.Atoi(q.Get("minutes"))
	if err != nil || !slices.Contains(allowedMinutes, minutes) {
		return 0, 0, fmt.Errorf("invalid data (minutes must be one of %v)", allowedMinutes)
	}
	return minReporters, minutes, nil
}

// handleListIPs returns IPs reported by at least min_reporters distinct users
// within the last `minutes` minutes, e.g. GET /api/v1/ips?min_reporters=5&minutes=360.
func handleListIPs(w http.ResponseWriter, r *http.Request) {
	rawKey := apiKeyFromRequest(r)
	if rawKey == "" {
		// The challenge lets clients that only send Basic auth after a 401
		// (e.g. uclient-fetch on OpenWrt) retry with credentials.
		w.Header().Set("WWW-Authenticate", `Basic realm="api"`)
		http.Error(w, "missing X-API-Key header or Basic auth", http.StatusUnauthorized)
		return
	}
	if _, _, err := authenticateAPIKey(r.Context(), rawKey); err != nil {
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	minReporters, minutes, err := parseListParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Precomputed by the list generator (lists.go); computed here only until
	// the first generation or when Redis is unavailable.
	if serveCachedList(w, r, minReporters, minutes) {
		addLogNote(r, "from", "cache")
		addLogNote(r, "tier", fmt.Sprintf("%d/%d", minReporters, minutes))
		return
	}
	defer addLogNote(r, "tier", fmt.Sprintf("%d/%d", minReporters, minutes))
	defer addLogNote(r, "from", "db")

	debugf("list %d/%d: not in Redis, computing from PostgreSQL", minReporters, minutes)
	result, err := queryIPList(r.Context(), minutes, minReporters)
	if err != nil {
		log.Printf("list query failed: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	p, err := newListPayload(result)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeListPayload(w, r, p)
}

// queryIPList returns the addresses reported by at least minReporters
// distinct users within the last `minutes` minutes, the most reported first.
func queryIPList(ctx context.Context, minutes, minReporters int) ([]ipListEntry, error) {
	rows, err := db.Query(ctx, `
		SELECT abbrev(ip), COUNT(DISTINCT user_id) AS reporters,
			COALESCE(array_agg(DISTINCT source ORDER BY source) FILTER (WHERE source IS NOT NULL), '{}')
		FROM ip_reports
		WHERE reported_at > now() - $1::int * interval '1 minute'
		GROUP BY ip
		HAVING COUNT(DISTINCT user_id) >= $2
		ORDER BY reporters DESC, ip`,
		minutes, minReporters,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []ipListEntry{}
	for rows.Next() {
		var e ipListEntry
		if err := rows.Scan(&e.IP, &e.DistinctReporters, &e.Sources); err != nil {
			return nil, err
		}
		e.Country = countryOf(e.IP)
		result = append(result, e)
	}
	return result, rows.Err()
}

// ---------- HELPERS ----------

// apiKeyFromRequest returns the API key from the X-API-Key header or, for
// clients that cannot set custom headers, from the Basic auth password
// (the username is ignored).
func apiKeyFromRequest(r *http.Request) string {
	if key := r.Header.Get("X-API-Key"); key != "" {
		return key
	}
	if _, password, ok := r.BasicAuth(); ok {
		return password
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// acceptsGzip: "Accept-Encoding: gzip" or "gzip, deflate, br" (q=0 disables).
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return strings.ReplaceAll(strings.TrimSpace(params), " ", "") != "q=0"
		}
	}
	return false
}
