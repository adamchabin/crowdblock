// Package list handles the precomputed IP blocklist: generation, caching in
// Redis, and delta sync for GET /api/v1/ips.
package list

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"server/internal/db"
	"server/internal/logging"

	"github.com/redis/go-redis/v9"
)

// The lists of GET /api/v1/ips are precomputed for every allowed
// (min_reporters, minutes) combination and stored in Redis, each in two
// versions – plain JSON and gzip – with the ETag of the content:
//
//	ips:<min_reporters>:<minutes>:json
//	ips:<min_reporters>:<minutes>:gz
//	ips:<min_reporters>:<minutes>:etag
//
// Requests are then served without touching PostgreSQL. With several server
// instances only the one holding ips:lock generates in a given round. Each
// generation also stores what delta sync needs (see delta.go).

const (
	defaultListRefresh = time.Minute
	listLockKey        = "ips:lock"
	listVersionKey     = "ips:version"
	listHistory        = 60 // generations kept for deltas (1 h at LIST_REFRESH=1m)
	maxDeltaFraction   = 2  // a delta larger than count/2 is sent as the full list
)

// The list is served for fixed thresholds only, so that the server can
// precompute and cache every combination (5 × 4 lists).
var (
	// AllowedMinReporters are the accepted min_reporters values.
	AllowedMinReporters = []int{1, 5, 10, 20, 50}
	// AllowedMinutes are the accepted minutes values.
	AllowedMinutes = []int{60, 360, 1440, 10080} // 1 h, 6 h, 24 h, 7 days
)

// Counters of the served lists (for the statistics endpoint).
var (
	ListFull        atomic.Int64
	ListNotModified atomic.Int64
	ListDelta       atomic.Int64
	ListMiss        atomic.Int64
)

// Meta describes what was served, for the request log.
type Meta struct {
	Kind     string // "full", "diff" or "not-modified"
	Encoding string // "gzip" or "plain"
	Records  int    // number of IP records sent
}

// listPayload is one precomputed list.
type listPayload struct {
	plain []byte // JSON, as written by writeJSON
	gz    []byte
	etag  string
}

// listKey returns the Redis key of one list version/kind.
func listKey(minReporters, minutes int, kind string) string {
	return fmt.Sprintf("ips:%d:%d:%s", minReporters, minutes, kind)
}

// listRefreshInterval returns the interval between list generations.
func listRefreshInterval() time.Duration {
	if v := os.Getenv("LIST_REFRESH"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			return d
		}
		slog.Warn("invalid LIST_REFRESH, using default", "value", v, "default", defaultListRefresh)
	}
	return defaultListRefresh
}

// RefreshInterval returns the interval between list generations.
func RefreshInterval() time.Duration { return listRefreshInterval() }

// encodeList writes the list as a JSON array with one entry per line:
//
//	[
//	{"ip":"1.2.3.4","distinct_reporters":3},
//	{"ip":"5.6.7.8","distinct_reporters":1}
//	]
//
// Plain JSON for any client, and the OpenWrt client can parse it line by
// line while unpacking, without holding the whole list in memory.
func encodeList(entries []db.IPListEntry) ([]byte, error) {
	if len(entries) == 0 {
		return []byte("[]\n"), nil // not "null": clients expect an array
	}

	var b bytes.Buffer
	b.WriteString("[\n")
	for i, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		b.Write(line)
		if i < len(entries)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]\n")
	return b.Bytes(), nil
}

// NewListPayload builds one list version (plain, gzip, ETag).
func NewListPayload(entries []db.IPListEntry) (listPayload, error) {
	encoded, err := encodeList(entries)
	if err != nil {
		return listPayload{}, err
	}
	plain := bytes.NewBuffer(encoded)

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(plain.Bytes()); err != nil {
		return listPayload{}, err
	}
	if err := zw.Close(); err != nil {
		return listPayload{}, err
	}

	// Part of the API contract: clients compute the same value from the
	// unpacked body (the OpenWrt client with sha256sum) to send If-None-Match.
	sum := sha256.Sum256(plain.Bytes())
	return listPayload{
		plain: plain.Bytes(),
		gz:    gz.Bytes(),
		etag:  `"` + hex.EncodeToString(sum[:8]) + `"`,
	}, nil
}

// atLeast returns the leading entries with at least min reporters. `entries`
// must be sorted by DistinctReporters, descending (as db.QueryIPList returns).
func atLeast(entries []db.IPListEntry, min int) []db.IPListEntry {
	n := 0
	for n < len(entries) && entries[n].DistinctReporters >= min {
		n++
	}
	return entries[:n]
}

// buildLists computes the lists of every allowed min_reporters for one
// window from the entries with min_reporters 1 – one database query per
// window instead of five. Keyed by min_reporters.
func buildLists(all []db.IPListEntry) (map[int]listPayload, error) {
	lists := map[int]listPayload{}
	for _, min := range AllowedMinReporters {
		p, err := NewListPayload(atLeast(all, min))
		if err != nil {
			return nil, err
		}
		lists[min] = p
	}
	return lists, nil
}

// RunListGenerator regenerates all lists every interval until ctx is done.
func RunListGenerator(ctx context.Context, interval time.Duration) error {
	instance := fmt.Sprintf("%s:%d", hostname(), os.Getpid())
	failing := false

	t := time.NewTimer(interval)
	defer t.Stop()

	for {
		start := time.Now()
		n, err := GenerateLists(ctx, instance, interval)
		switch {
		case err != nil && !failing:
			// Logged once until it works again (Redis down = every round).
			slog.Error("list generation failed", "err", err)
			failing = true
		case err == nil && failing:
			slog.Info("list generation recovered", "lists", n, "duration", time.Since(start).Round(time.Millisecond))
			failing = false
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			t.Reset(interval)
		}
	}
}

// GenerateLists returns the number of lists stored; 0 when another instance
// holds the lock for this round.
func GenerateLists(ctx context.Context, instance string, interval time.Duration) (int, error) {
	// Held for a bit less than a round, so that the next round can take it.
	start := time.Now()
	ok, err := db.RDB.SetNX(ctx, listLockKey, instance, interval-interval/10).Result()
	if err != nil {
		return 0, fmt.Errorf("redis lock: %w", err)
	}
	if !ok {
		logging.Debugf("lists: round skipped, lock held by %s", instance)
		return 0, nil
	}

	// The previous generation, for the diffs of delta sync (delta.go).
	prev, err := previousLists(ctx)
	if err != nil {
		return 0, err
	}
	version, err := db.RDB.Incr(ctx, listVersionKey).Result()
	if err != nil {
		return 0, fmt.Errorf("redis version: %w", err)
	}
	logging.Debugf("lists: round %d started (lock taken by %s, %d previous lists in Redis)", version, instance, len(prev))

	lists := map[int]map[int]listPayload{}        // minutes -> min_reporters -> list
	entries := map[int]map[int][]db.IPListEntry{} // the same, decoded
	n := 0
	for _, minutes := range AllowedMinutes {
		queryStart := time.Now()
		all, err := db.QueryIPList(ctx, minutes, 1)
		if err != nil {
			return 0, fmt.Errorf("query (minutes=%d): %w", minutes, err)
		}
		logging.Debugf("lists: round %d window %dm: %d addresses, query %s", version, minutes, len(all), time.Since(queryStart).Round(time.Microsecond))
		if lists[minutes], err = buildLists(all); err != nil {
			return 0, err
		}
		entries[minutes] = map[int][]db.IPListEntry{}
		for _, min := range AllowedMinReporters {
			entries[minutes][min] = atLeast(all, min)
		}
		n += len(lists[minutes])
	}

	// One transaction: a client never gets the gzip and plain versions (or
	// the ETag, or a diff) of different generations. Expire if the generator
	// stops.
	ttl := 5 * interval
	_, err = db.RDB.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for minutes, window := range lists {
			for min, p := range window {
				pipe.Set(ctx, listKey(min, minutes, "json"), p.plain, ttl)
				pipe.Set(ctx, listKey(min, minutes, "gz"), p.gz, ttl)
				pipe.Set(ctx, listKey(min, minutes, "etag"), p.etag, ttl)

				old, known := prev[listKey(min, minutes, "json")]
				if err := storeDeltaData(ctx, pipe, min, minutes, version, p.etag,
					old, entries[minutes][min], known, interval); err != nil {
					return err
				}

				logging.Debugf("lists: round %d list %d/%d: %d addresses, etag %s, %dB plain / %dB gzip",
					version, min, minutes, len(entries[minutes][min]), p.etag, len(p.plain), len(p.gz))
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("redis write: %w", err)
	}
	logging.Debugf("lists: round %d done, %d lists in %s", version, n, time.Since(start).Round(time.Millisecond))
	return n, nil
}

// previousLists returns the lists of the previous generation still in Redis,
// decoded, keyed by their json key.
func previousLists(ctx context.Context) (map[string][]db.IPListEntry, error) {
	var keys []string
	for _, minutes := range AllowedMinutes {
		for _, min := range AllowedMinReporters {
			keys = append(keys, listKey(min, minutes, "json"))
		}
	}

	vals, err := db.RDB.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis read: %w", err)
	}

	prev := map[string][]db.IPListEntry{}
	for i, v := range vals {
		var list []db.IPListEntry
		if s, ok := v.(string); ok && json.Unmarshal([]byte(s), &list) == nil {
			prev[keys[i]] = list
		}
	}
	return prev, nil
}

// ServeCachedList writes the precomputed list and returns what it served, or
// returns ok=false (nothing written) when the list is not in Redis.
func ServeCachedList(w http.ResponseWriter, r *http.Request, minReporters, minutes int) (Meta, bool) {
	ctx := r.Context()

	// On a Redis error just fall back: the generator already logs it, once.
	etag, err := db.RDB.Get(ctx, listKey(minReporters, minutes, "etag")).Result()
	if err != nil {
		logging.Debugf("list %d/%d: no etag in Redis (%v)", minReporters, minutes, err)
		ListMiss.Add(1)
		return Meta{}, false
	}

	have := r.Header.Get("If-None-Match")
	if have == etag {
		ListNotModified.Add(1)
		return writeList(w, r, etag, nil, false), true
	}

	// Delta sync only for clients asking for it: others couldn't parse it.
	if have != "" && r.URL.Query().Get("delta") == "1" {
		if meta, ok := serveDelta(w, r, minReporters, minutes, have, etag); ok {
			ListDelta.Add(1)
			return meta, true
		}
	}

	gzipped := acceptsGzip(r)
	kind := "json"
	if gzipped {
		kind = "gz"
	}
	body, err := db.RDB.Get(ctx, listKey(minReporters, minutes, kind)).Bytes()
	if err != nil {
		ListMiss.Add(1)
		return Meta{}, false
	}

	ListFull.Add(1)
	return writeList(w, r, etag, body, gzipped), true
}

// WriteListPayload sends a list computed for this request (no cache).
func WriteListPayload(w http.ResponseWriter, r *http.Request, p listPayload) Meta {
	if acceptsGzip(r) {
		return writeList(w, r, p.etag, p.gz, true)
	}
	return writeList(w, r, p.etag, p.plain, false)
}

// writeList sends one version of a list, or 304 if the client has it.
func writeList(w http.ResponseWriter, r *http.Request, etag string, body []byte, gzipped bool) Meta {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Add("Vary", "Accept-Encoding")
	h.Set("ETag", etag)
	// Clients may keep the list but must ask whether it changed.
	h.Set("Cache-Control", "no-cache")

	meta := Meta{Kind: "full", Encoding: "plain"}
	if gzipped {
		meta.Encoding = "gzip"
	}

	// The client's copy is current: 304 and no body at all.
	if r.Header.Get("If-None-Match") == etag {
		meta.Kind = "not-modified"
		meta.Records = 0
		w.WriteHeader(http.StatusNotModified)
		return meta
	}

	meta.Records = bytes.Count(body, []byte(`"ip":`))
	if gzipped {
		h.Set("Content-Encoding", "gzip")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return meta
}

// hostname returns the OS hostname.
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
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
