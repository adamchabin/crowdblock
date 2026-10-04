package main

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

const (
	maxAPIKeysPerUser = 10
	blacklistWindow   = time.Hour
	blacklistThreshold = 5
	apiKeyCacheTTL    = 60 * time.Second
)

// Set at build time: go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

var db *pgxpool.Pool
var rdb *redis.Client

func main() {
	log.Printf("crowdblock server %s", version)

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
	mux.HandleFunc("POST /api/v1/register", handleRegister)
	mux.HandleFunc("POST /api/v1/api-keys", handleCreateAPIKey)
	mux.HandleFunc("POST /api/v1/reports", handleReportIP)
	mux.HandleFunc("GET /api/v1/ips", handleListIPs)

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, logRequests(mux)))
}

// logRequests logs every incoming HTTP request to the console: method, path,
// remote address, response status, bytes received / sent, and duration.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		body := &countingReader{ReadCloser: r.Body}
		r.Body = body

		next.ServeHTTP(sw, r)

		log.Printf("%s %s %s %d in=%s out=%s %s", r.RemoteAddr, r.Method, r.URL.Path, sw.status,
			formatBytes(body.n), formatBytes(sw.n), time.Since(start))
	})
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
	Password string `json:"password"`
}

func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || len(req.Password) < 8 {
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

// ---------- API KEYS ----------
// Basic Auth (email/password) — key management only

type createKeyRequest struct {
	Name string `json:"name"`
}

func handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	email, password, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="api"`)
		http.Error(w, "authorization required", http.StatusUnauthorized)
		return
	}

	var userID, passwordHash string
	err := db.QueryRow(r.Context(),
		`SELECT id, password_hash FROM users WHERE email = $1`, email,
	).Scan(&userID, &passwordHash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
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

	if _, err := tx.Exec(r.Context(),
		`INSERT INTO api_keys (user_id, key_prefix, key_hash, name) VALUES ($1, $2, $3, $4)`,
		userID, keyPrefix, keyHash, req.Name,
	); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// full key is shown only once, at creation time
	writeJSON(w, http.StatusCreated, map[string]string{"api_key": rawKey})
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

	writeJSON(w, http.StatusCreated, map[string]any{"accepted": true, "blacklisted": blacklisted})
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
			return id, uid, nil
		}
	} else if cacheErr != redis.Nil {
		log.Printf("redis GET error for %s: %v", cacheKey, cacheErr)
	}

	err = db.QueryRow(ctx,
		`SELECT id, user_id FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`, hash,
	).Scan(&apiKeyID, &userID)
	if err != nil {
		return "", "", errors.New("key invalid or revoked")
	}

	if setErr := rdb.Set(ctx, cacheKey, apiKeyID+"|"+userID, apiKeyCacheTTL).Err(); setErr != nil {
		log.Printf("redis SET error for %s: %v", cacheKey, setErr)
	}

	return
}

// maybeBlacklist records the report in a Redis sorted set (member = user_id,
// score = report time) keyed per IP, trims entries older than blacklistWindow,
// and counts the remainder to get the number of distinct recent reporters —
// this replaces the per-report COUNT(DISTINCT user_id) aggregate that used to
// run against postgres on every single call. If the threshold is reached, the
// (rare) blacklist upsert still goes to postgres for durable storage.
func maybeBlacklist(ctx context.Context, ip, userID string) (bool, error) {
	key := "ip:reports:" + ip
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
		return
	}

	result, err := queryIPList(r.Context(), minutes, minReporters)
	if err != nil {
		log.Printf("list query failed: %v", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	writeJSONCompressed(w, r, http.StatusOK, result)
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

// writeJSONCompressed is writeJSON with gzip for clients that accept it - for
// large responses (the IP list: ~300 KiB for 5000 entries, ~45 KiB gzipped).
func writeJSONCompressed(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r) {
		writeJSON(w, status, v)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(status)

	gz := gzip.NewWriter(w)
	_ = json.NewEncoder(gz).Encode(v)
	_ = gz.Close()
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
