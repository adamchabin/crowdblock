package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"server/internal/geoip"
	"server/internal/logging"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

const (
	// MaxAPIKeysPerUser limits the active API keys of one user.
	MaxAPIKeysPerUser = 20
	// BlacklistWindow is the window in which distinct reporters are counted.
	BlacklistWindow = time.Hour
	// BlacklistThreshold is the number of distinct reporters that blacklists an IP.
	BlacklistThreshold = 5
	// APIKeyCacheTTL is how long a resolved API key stays in the cache.
	APIKeyCacheTTL = 60 * time.Second
)

// dummyHash makes the login of a nonexistent user cost as much as a real
// one, so response time doesn't reveal which e-mails are registered.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.DefaultCost)

// SourcePattern validates report sources (short identifiers chosen by the reporter).
var SourcePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// API key auth counters (Redis hits vs database lookups).
var (
	KeyHits   atomic.Int64
	KeyMisses atomic.Int64
)

// IPListEntry is one entry of the IP blocklist.
type IPListEntry struct {
	IP                string   `json:"ip"`
	DistinctReporters int      `json:"distinct_reporters"`
	Country           string   `json:"country,omitempty"`      // ISO 3166-1 alpha-2, e.g. "CN"
	CountryName       string   `json:"country_name,omitempty"` // English name, e.g. "China"
	Sources           []string `json:"sources,omitempty"`      // e.g. ["auth", "fail2ban"]
}

// GenerateAPIKey returns a new API key with its hash and prefix.
func GenerateAPIKey() (raw, hash, prefix string, err error) {
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

// AuthenticateUser checks the email/password pair and returns the user's id.
// The second result is false when the credentials don't match.
func AuthenticateUser(ctx context.Context, email, password string) (userID string, ok bool) {
	passwordHash := string(dummyHash)
	err := DB.QueryRow(ctx,
		`SELECT id, password_hash FROM users WHERE email = $1`, email,
	).Scan(&userID, &passwordHash)
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil || err != nil {
		return "", false
	}
	return userID, true
}

// ForgetAPIKeys drops the cached authentication of keys (by key_hash), so
// a revoked key stops working at once, not after APIKeyCacheTTL.
func ForgetAPIKeys(ctx context.Context, hashes ...string) {
	keys := make([]string, len(hashes))
	for i, h := range hashes {
		keys[i] = "apikey:" + h
	}
	if len(keys) == 0 {
		return
	}
	if err := RDB.Del(ctx, keys...).Err(); err != nil {
		slog.Warn("redis DEL failed", "keys", len(keys), "err", err)
	}
}

// ReportersKey is the Redis sorted set of recent reporters of one IP.
func ReportersKey(ip string) string { return "ip:reports:" + ip }

// MaybeBlacklist records the report in a Redis sorted set (member = user_id,
// score = report time) keyed per IP, trims entries older than BlacklistWindow,
// and counts the remainder to get the number of distinct recent reporters —
// this replaces the per-report COUNT(DISTINCT user_id) aggregate that used to
// run against postgres on every single call. If the threshold is reached, the
// (rare) blacklist upsert still goes to postgres for durable storage.
func MaybeBlacklist(ctx context.Context, ip, userID string) (bool, error) {
	key := ReportersKey(ip)
	now := time.Now()
	cutoff := now.Add(-BlacklistWindow)

	// One round trip instead of four: ZADD, ZREMRANGEBYSCORE and EXPIRE don't
	// depend on each other's results, and ZCARD only needs the final state.
	cmds, err := RDB.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.Unix()), Member: userID})
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(cutoff.Unix(), 10))
		pipe.Expire(ctx, key, BlacklistWindow)
		pipe.ZCard(ctx, key)
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("redis pipeline: %w", err)
	}
	distinctReporters := int(cmds[3].(*redis.IntCmd).Val())

	if distinctReporters < BlacklistThreshold {
		return false, nil
	}

	_, err = DB.Exec(ctx, `
		INSERT INTO blacklist (ip, first_seen, last_seen, distinct_reporters)
		VALUES ($1, now(), now(), $2)
		ON CONFLICT (ip) DO UPDATE
		SET last_seen = now(), distinct_reporters = EXCLUDED.distinct_reporters`,
		ip, distinctReporters,
	)
	return err == nil, err
}

// AuthenticateAPIKey resolves a raw API key to its (apiKeyID, userID) pair.
// Successful lookups are cached in Redis for APIKeyCacheTTL, so repeated
// requests from the same key don't hit postgres until the cache entry expires.
func AuthenticateAPIKey(ctx context.Context, rawKey string) (apiKeyID, userID string, err error) {
	sum := sha256.Sum256([]byte(rawKey))
	hash := hex.EncodeToString(sum[:])
	cacheKey := "apikey:" + hash

	if cached, cacheErr := RDB.Get(ctx, cacheKey).Result(); cacheErr == nil {
		if id, uid, ok := strings.Cut(cached, "|"); ok {
			KeyHits.Add(1)
			logging.Debugf("api key %s: from cache", id)
			return id, uid, nil
		}
	} else if cacheErr != redis.Nil {
		slog.Warn("redis GET failed", "key", cacheKey, "err", cacheErr)
		// If Redis is down and Postgres is also unreachable, return 503.
		if IsRedisError(cacheErr) {
			if dbPingErr := DB.Ping(ctx); dbPingErr != nil && IsDBError(dbPingErr) {
				return "", "", fmt.Errorf("redis and postgres unavailable: %w", cacheErr)
			}
		}
	}

	KeyMisses.Add(1)
	err = DB.QueryRow(ctx,
		`SELECT id, user_id FROM api_keys WHERE key_hash = $1 AND revoked_at IS NULL`, hash,
	).Scan(&apiKeyID, &userID)
	if err != nil {
		if IsDBError(err) {
			return "", "", fmt.Errorf("postgres unavailable: %w", err)
		}
		logging.Debugf("api key %s…: invalid or revoked", rawKey[:min(len(rawKey), 8)])
		return "", "", errors.New("key invalid or revoked")
	}
	logging.Debugf("api key %s: from database, cached for %s", apiKeyID, APIKeyCacheTTL)

	if setErr := RDB.Set(ctx, cacheKey, apiKeyID+"|"+userID, APIKeyCacheTTL).Err(); setErr != nil {
		slog.Warn("redis SET failed", "key", cacheKey, "err", setErr)
	}

	return
}

// QueryIPList returns the addresses reported by at least minReporters
// distinct users within the last `minutes` minutes, the most reported first.
func QueryIPList(ctx context.Context, minutes, minReporters int) ([]IPListEntry, error) {
	rows, err := DB.Query(ctx, `
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

	result := []IPListEntry{}
	for rows.Next() {
		var e IPListEntry
		if err := rows.Scan(&e.IP, &e.DistinctReporters, &e.Sources); err != nil {
			return nil, err
		}
		e.Country, e.CountryName = geoip.CountryOf(netip.MustParseAddr(e.IP), geoip.GeoDB)
		result = append(result, e)
	}
	return result, rows.Err()
}

// PruneReports deletes ip_reports rows older than `retain`. Reports past the
// largest list window (see list.AllowedMinutes) are never read again, but
// without this the table (and its indexes) would grow forever.
func PruneReports(ctx context.Context, retain time.Duration) (int64, error) {
	tag, err := DB.Exec(ctx,
		`DELETE FROM ip_reports WHERE reported_at < now() - $1::int * interval '1 second'`,
		int(retain.Seconds()),
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RunReportPruner deletes expired ip_reports rows every `interval` until ctx
// is done.
func RunReportPruner(ctx context.Context, retain, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			n, err := PruneReports(ctx, retain)
			if err != nil {
				slog.Error("report pruning failed", "err", err)
				continue
			}
			if n > 0 {
				slog.Info("pruned old ip_reports", "deleted", n, "retain", retain)
			}
		}
	}
}

// IsPublicIP rejects addresses that must never reach a blocklist: private,
// loopback, link-local, multicast, unspecified. (CIDR input fails ParseAddr.)
func IsPublicIP(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.Zone() == "" && a.IsGlobalUnicast() && !a.IsPrivate()
}

// ErrNoRows is returned when a query matches nothing.
var ErrNoRows = pgx.ErrNoRows
