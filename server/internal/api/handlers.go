// Package api provides the HTTP handlers, middleware and helpers of the
// crowdblock server.
package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"server/internal/db"
	"server/internal/geoip"
	"server/internal/list"
	"server/internal/logging"
	"server/internal/stats"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

const (
	// MaxBodyBytes is the maximum request body size.
	MaxBodyBytes = 4 << 10 // all request bodies are tiny JSON objects
	// MaxGeoIPBatch is the maximum number of addresses in one geoip batch.
	MaxGeoIPBatch = 100
	// registerPerHour limits the registrations from one client address.
	// shortcut: no per-email limit besides the cooldown, add one if abused.
	registerPerHour = 5
	// registerCooldown is the time between mails to the same address.
	registerCooldown = 10 * time.Minute
)

// dummyHash makes the login of a nonexistent user cost as much as a real
// one, so response time doesn't reveal which e-mails are registered.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.DefaultCost)

// registerRequest is the body of POST /api/v1/register.
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"` // optional: when empty, a password is generated and e-mailed
}

// createKeyRequest is the body of POST /api/v1/api-keys.
type createKeyRequest struct {
	Name string `json:"name"`
}

// reportRequest is the body of POST /api/v1/reports.
type reportRequest struct {
	IP     string `json:"ip"`
	Source string `json:"source"` // optional: what detected the attack, e.g. "auth"
}

// uiHTML is the embedded UI page.
//
//go:embed ui.html
var uiHTML []byte

// worldJSON holds simplified country shapes with ISO 3166-1 alpha-2 codes
// (generated once into world.json), matched client-side against stats.
//
//go:embed world.json
var worldJSON []byte

// HandleHealth reports whether the server and its dependencies (Postgres,
// Redis) are reachable. Returns 200 when everything is up, 503 when a
// dependency is down, with a JSON body naming the failing service.
func HandleHealth(w http.ResponseWriter, r *http.Request) {
	type health struct {
		Status   string `json:"status"`
		Postgres string `json:"postgres"`
		Redis    string `json:"redis"`
	}

	h := health{Status: "ok", Postgres: "up", Redis: "up"}

	if err := db.DB.Ping(r.Context()); err != nil {
		h.Status = "degraded"
		h.Postgres = "down"
	}

	if err := db.RDB.Ping(r.Context()).Err(); err != nil {
		h.Status = "degraded"
		h.Redis = "down"
	}

	if h.Status == "degraded" {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h)
}

// HandleRegister handles POST /api/v1/register.
func HandleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		http.Error(w, "invalid data (email required)", http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		registerByEmail(w, r, req.Email)
		return
	}
	// bcrypt only uses the first 72 bytes and newer x/crypto rejects longer input.
	if len(req.Password) < 8 || len(req.Password) > 72 {
		http.Error(w, "invalid data (email + password, 8-72 bytes)", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	var userID string
	err = db.DB.QueryRow(r.Context(),
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) RETURNING id`,
		req.Email, string(hash),
	).Scan(&userID)
	if err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		if db.IsUniqueViolation(err) {
			http.Error(w, "email already taken", http.StatusConflict)
			return
		}
		slog.Error("register: insert user failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
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
	if missing := smtpMissing(); len(missing) > 0 {
		slog.Warn("register by e-mail unavailable", "missing", strings.Join(missing, ", "))
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
	n, err := db.RDB.Incr(ctx, ipKey).Result()
	if err != nil {
		slog.Warn("redis INCR failed", "key", ipKey, "err", err)
		db.ServiceUnavailable(w, "redis", err)
		return
	}
	if n == 1 {
		db.RDB.Expire(ctx, ipKey, time.Hour)
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
	if fresh, err := db.RDB.SetNX(ctx, mailKey, 1, registerCooldown).Result(); err != nil || !fresh {
		if err != nil && db.IsRedisError(err) {
			db.ServiceUnavailable(w, "redis", err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"message": reply})
		return
	}

	var userID string
	err = db.DB.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 ON CONFLICT (email) DO NOTHING RETURNING id`, email, string(hash)).Scan(&userID)
	switch {
	case errors.Is(err, pgx.ErrNoRows): // already registered: send nothing
	case err != nil:
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		db.RDB.Del(ctx, mailKey)
		slog.Error("register failed", "email", email, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	default:
		// Sent in the background so the response time doesn't depend on SMTP.
		// If the mail can't be sent the account is removed again, so the user
		// can register again instead of owning a password nobody received.
		// Uses context.Background(), not r.Context(): the request (and its
		// context, via TimeoutMiddleware) is done as soon as this handler
		// returns, which happens right after this goroutine starts, so a
		// context derived from it would already be canceled by the time the
		// cleanup below runs, silently breaking it.
		go func(userID, email, password, mailKey string) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := sendPasswordMail(email, password); err != nil {
				slog.Warn("registration mail failed", "email", email, "err", err)
				_, _ = db.DB.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
				db.RDB.Del(ctx, mailKey)
			}
		}(userID, email, password, mailKey)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": reply})
}

// HandleCreateAPIKey handles POST /api/v1/api-keys.
func HandleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}

	var req createKeyRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // name is optional

	tx, err := db.DB.Begin(r.Context())
	if err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var activeCount int
	if err := tx.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM api_keys WHERE user_id = $1 AND revoked_at IS NULL`, userID,
	).Scan(&activeCount); err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if activeCount >= db.MaxAPIKeysPerUser {
		http.Error(w, "limit of 10 active keys reached", http.StatusForbidden)
		return
	}

	rawKey, keyHash, keyPrefix, err := db.GenerateAPIKey()
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	var keyID string
	if err := tx.QueryRow(r.Context(),
		`INSERT INTO api_keys (user_id, key_prefix, key_hash, name) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, keyPrefix, keyHash, req.Name,
	).Scan(&keyID); err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// full key is shown only once, at creation time
	writeJSON(w, http.StatusCreated, map[string]string{"id": keyID, "api_key": rawKey})
}

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
	err := db.DB.QueryRow(r.Context(),
		`SELECT id, password_hash FROM users WHERE email = $1`, email,
	).Scan(&userID, &passwordHash)
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil || err != nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return "", false
	}
	return userID, true
}

// HandleListAPIKeys handles GET /api/v1/api-keys: the user's active keys
// (ids, never the keys).
func HandleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}
	rows, err := db.DB.Query(r.Context(),
		`SELECT id, key_prefix, COALESCE(name, ''), created_at FROM api_keys
		 WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at`, userID)
	if err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
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
			if db.IsDBError(err) {
				db.ServiceUnavailable(w, "postgres", err)
				return
			}
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		keys = append(keys, k)
	}
	if rows.Err() != nil {
		if db.IsDBError(rows.Err()) {
			db.ServiceUnavailable(w, "postgres", rows.Err())
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// HandleRevokeAPIKey handles DELETE /api/v1/api-keys/{id}.
func HandleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}

	var keyHash string
	err := db.DB.QueryRow(r.Context(),
		`UPDATE api_keys SET revoked_at = now()
		 WHERE id::text = $1 AND user_id = $2 AND revoked_at IS NULL RETURNING key_hash`,
		r.PathValue("id"), userID,
	).Scan(&keyHash)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "no such active key", http.StatusNotFound)
		return
	}
	if err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	db.ForgetAPIKeys(r.Context(), keyHash)
	w.WriteHeader(http.StatusNoContent)
}

// HandleDeleteAccount handles DELETE /api/v1/account: deletes the user with
// all keys and reports (ON DELETE CASCADE). Reports are gone from the lists at
// the next refresh; entries already in the blacklist table stay.
func HandleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID, ok := authenticateUser(w, r)
	if !ok {
		return
	}

	var hashes []string
	if err := db.DB.QueryRow(r.Context(),
		`SELECT COALESCE(array_agg(key_hash), '{}') FROM api_keys WHERE user_id = $1`, userID,
	).Scan(&hashes); err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// The user must also stop counting towards the blacklist threshold:
	// drop it from the reporter sets of the IPs it reported in the window.
	var ips []string
	if rows, err := db.DB.Query(r.Context(),
		`SELECT DISTINCT host(ip) FROM ip_reports
		 WHERE user_id = $1 AND reported_at > now() - $2::int * interval '1 second'`,
		userID, int(db.BlacklistWindow.Seconds())); err == nil {
		ips, _ = pgx.CollectRows(rows, pgx.RowTo[string])
	}

	if _, err := db.DB.Exec(r.Context(), `DELETE FROM users WHERE id = $1`, userID); err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	if len(ips) > 0 {
		if _, err := db.RDB.Pipelined(r.Context(), func(pipe redis.Pipeliner) error {
			for _, ip := range ips {
				pipe.ZRem(r.Context(), db.ReportersKey(ip), userID)
			}
			return nil
		}); err != nil {
			slog.Warn("redis ZREM failed", "user", userID, "err", err)
		}
	}
	db.ForgetAPIKeys(r.Context(), hashes...)
	w.WriteHeader(http.StatusNoContent)
}

// HandleReportIP handles POST /api/v1/reports.
func HandleReportIP(w http.ResponseWriter, r *http.Request) {
	rawKey := r.Header.Get("X-API-Key")
	if rawKey == "" {
		http.Error(w, "missing X-API-Key header", http.StatusUnauthorized)
		return
	}

	apiKeyID, userID, err := db.AuthenticateAPIKey(r.Context(), rawKey)
	if err != nil {
		if db.IsDBError(err) || db.IsRedisError(err) {
			db.ServiceUnavailable(w, "auth", err)
			return
		}
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		http.Error(w, "invalid data (ip field required)", http.StatusBadRequest)
		return
	}
	addr, err := netip.ParseAddr(req.IP)
	if err != nil || !db.IsPublicIP(addr) {
		http.Error(w, "invalid data (ip must be a public IPv4/IPv6 address)", http.StatusBadRequest)
		return
	}
	req.IP = addr.Unmap().String()
	if req.Source != "" && !db.SourcePattern.MatchString(req.Source) {
		http.Error(w, "invalid data (source: up to 32 characters a-z, 0-9, _ and -)", http.StatusBadRequest)
		return
	}

	if _, err := db.DB.Exec(r.Context(),
		`INSERT INTO ip_reports (ip, api_key_id, user_id, source) VALUES ($1, $2, $3, NULLIF($4, ''))`,
		req.IP, apiKeyID, userID, req.Source,
	); err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		// The input was validated above, so a remaining error is a server fault.
		slog.Error("report: insert failed", "ip", req.IP, "err", err)
		// TODO(adam): cap reports per user per IP (security.md #6).
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	blacklisted, err := db.MaybeBlacklist(r.Context(), req.IP, userID)
	if err != nil {
		if db.IsRedisError(err) {
			slog.Warn("redis unavailable, proceeding without blacklist", "ip", req.IP, "err", err)
		} else if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		} else {
			slog.Warn("blacklist aggregation failed", "ip", req.IP, "err", err)
		}
	}

	logging.Debugf("report %s source=%q by user %s (key %s)", req.IP, req.Source, userID, apiKeyID)
	writeJSON(w, http.StatusCreated, map[string]any{"accepted": true, "blacklisted": blacklisted})
}

// HandleListIPs handles GET /api/v1/ips: the addresses reported by at least
// min_reporters distinct users within the last `minutes` minutes.
func HandleListIPs(w http.ResponseWriter, r *http.Request) {
	rawKey := apiKeyFromRequest(r)
	if rawKey == "" {
		// The challenge lets clients that only send Basic auth after a 401
		// (e.g. uclient-fetch on OpenWrt) retry with credentials.
		w.Header().Set("WWW-Authenticate", `Basic realm="api"`)
		http.Error(w, "missing X-API-Key header or Basic auth", http.StatusUnauthorized)
		return
	}
	if _, _, err := db.AuthenticateAPIKey(r.Context(), rawKey); err != nil {
		if db.IsDBError(err) || db.IsRedisError(err) {
			db.ServiceUnavailable(w, "auth", err)
			return
		}
		http.Error(w, "invalid API key", http.StatusUnauthorized)
		return
	}

	minReporters, minutes, err := parseListParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Precomputed by the list generator (list package); computed here only
	// until the first generation or when Redis is unavailable.
	if meta, ok := list.ServeCachedList(w, r, minReporters, minutes); ok {
		addLogNote(r, "list", meta.Kind)
		addLogNote(r, "enc", meta.Encoding)
		setLogRecords(r, meta.Records)
		addLogNote(r, "from", "cache")
		addLogNote(r, "tier", fmt.Sprintf("%d/%d", minReporters, minutes))
		return
	}
	defer addLogNote(r, "tier", fmt.Sprintf("%d/%d", minReporters, minutes))
	defer addLogNote(r, "from", "db")

	logging.Debugf("list %d/%d: not in Redis, computing from PostgreSQL", minReporters, minutes)
	result, err := db.QueryIPList(r.Context(), minutes, minReporters)
	if err != nil {
		if db.IsDBError(err) {
			db.ServiceUnavailable(w, "postgres", err)
			return
		}
		slog.Error("list query failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	p, err := list.NewListPayload(result)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	meta := list.WriteListPayload(w, r, p)
	addLogNote(r, "list", meta.Kind)
	addLogNote(r, "enc", meta.Encoding)
	setLogRecords(r, meta.Records)
}

// HandleGeoIP handles GET /api/v1/geoip/{ip} – public, no auth required.
func HandleGeoIP(w http.ResponseWriter, r *http.Request) {
	ipStr := r.PathValue("ip")
	addr, err := netip.ParseAddr(ipStr)
	if err != nil || !db.IsPublicIP(addr) {
		http.Error(w, "invalid data (ip must be a public IPv4/IPv6 address)", http.StatusBadRequest)
		return
	}
	addr = addr.Unmap()

	logging.Debugf("geoip query: %s from %s", ipStr, r.RemoteAddr)
	code, name := geoip.CountryOf(addr, geoip.GeoDB)
	logging.Debugf("geoip result: %s (%s) for %s", code, name, ipStr)

	writeJSON(w, http.StatusOK, map[string]string{"ip": addr.String(), "country": code, "country_name": name})
}

// HandleGeoIPBatch handles POST /api/v1/geoip/batch – public, no auth
// required. Addresses that fail to parse or are not public get "Unknown",
// like a missed database lookup; the batch itself still returns 200.
func HandleGeoIPBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IPs []string `json:"ips"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid data (json object with ips list required)", http.StatusBadRequest)
		return
	}
	if len(req.IPs) > MaxGeoIPBatch {
		http.Error(w, "invalid data (up to "+strconv.Itoa(MaxGeoIPBatch)+" ips per request)", http.StatusBadRequest)
		return
	}
	out := make([]map[string]string, 0, len(req.IPs))
	for _, ipStr := range req.IPs {
		addr, err := netip.ParseAddr(ipStr)
		if err != nil || !db.IsPublicIP(addr) {
			out = append(out, map[string]string{"ip": ipStr, "country": "Unknown", "country_name": "Unknown"})
			continue
		}
		addr = addr.Unmap()
		code, name := geoip.CountryOf(addr, geoip.GeoDB)
		out = append(out, map[string]string{"ip": addr.String(), "country": code, "country_name": name})
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleStats handles GET /api/v1/stats.
func HandleStats(w http.ResponseWriter, r *http.Request) {
	stats.HandleStats(w, r)
}

// HandleUI handles GET /.
func HandleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(uiHTML)
}

// HandleWorld handles GET /api/v1/world.
func HandleWorld(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/geo+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(worldJSON)
}

// parseListParams validates min_reporters and minutes of GET /api/v1/ips.
func parseListParams(q url.Values) (minReporters, minutes int, err error) {
	minReporters, err = strconv.Atoi(q.Get("min_reporters"))
	if err != nil || !slices.Contains(list.AllowedMinReporters, minReporters) {
		return 0, 0, fmt.Errorf("invalid data (min_reporters must be one of %v)", list.AllowedMinReporters)
	}
	minutes, err = strconv.Atoi(q.Get("minutes"))
	if err != nil || !slices.Contains(list.AllowedMinutes, minutes) {
		return 0, 0, fmt.Errorf("invalid data (minutes must be one of %v)", list.AllowedMinutes)
	}
	return minReporters, minutes, nil
}
