package main

import (
	"context"
	_ "embed"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed ui.html
var uiHTML []byte

// Aggregates only: no addresses and no user data, so the endpoint is public.
type statsResponse struct {
	GeneratedAt    time.Time      `json:"generated_at"`
	Users          int            `json:"users"`
	ActiveKeys     int            `json:"active_keys"`
	Reports24h     int            `json:"reports_24h"`
	Reports7d      int            `json:"reports_7d"`
	UniqueIPs24h   int            `json:"unique_ips_24h"`
	Blacklisted24h int            `json:"blacklisted_24h"` // IPs that reached the threshold
	Hourly         []int          `json:"hourly"`          // reports per hour, last 24 h, oldest first
	Sources        map[string]int `json:"sources"`         // reports 24 h by source
	Countries      []countryCount `json:"countries"`       // top 10 by reported IPs, 24 h
	Cache          cacheStats     `json:"cache"`
}

// Cache counters live in this process (atomics, no cost on the hot path)
// and restart from zero with it.
// shortcut: per instance, not summed over several instances; move to Redis
// INCRs if that is ever needed.
var (
	cacheStart                                     = time.Now()
	keyHits, keyMisses                             atomic.Int64 // API key auth: Redis / postgres
	listFull, listNotModified, listDelta, listMiss atomic.Int64 // GET /ips: answered from Redis (3 ways) / computed from postgres
)

type cacheStats struct {
	Since  time.Time `json:"since"`
	APIKey struct {
		Hits   int64 `json:"hits"`
		Misses int64 `json:"misses"`
	} `json:"api_key"`
	Lists struct {
		Full        int64 `json:"full"`
		NotModified int64 `json:"not_modified"`
		Delta       int64 `json:"delta"`
		Misses      int64 `json:"misses"`
	} `json:"lists"`
	Redis *redisStats `json:"redis,omitempty"` // nil when Redis can't be asked
}

type redisStats struct {
	Keys           int64 `json:"keys"`
	UsedMemory     int64 `json:"used_memory"`
	KeyspaceHits   int64 `json:"keyspace_hits"`
	KeyspaceMisses int64 `json:"keyspace_misses"`
}

func computeCacheStats(ctx context.Context) cacheStats {
	var c cacheStats
	c.Since = cacheStart.UTC()
	c.APIKey.Hits, c.APIKey.Misses = keyHits.Load(), keyMisses.Load()
	c.Lists.Full, c.Lists.NotModified, c.Lists.Delta, c.Lists.Misses =
		listFull.Load(), listNotModified.Load(), listDelta.Load(), listMiss.Load()

	keys, err := rdb.DBSize(ctx).Result()
	info, err2 := rdb.Info(ctx, "memory", "stats").Result()
	if err == nil && err2 == nil {
		r := &redisStats{Keys: keys}
		for _, line := range strings.Split(info, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
			n, _ := strconv.ParseInt(v, 10, 64)
			if !ok {
				continue
			}
			switch k {
			case "used_memory":
				r.UsedMemory = n
			case "keyspace_hits":
				r.KeyspaceHits = n
			case "keyspace_misses":
				r.KeyspaceMisses = n
			}
		}
		c.Redis = r
	}
	return c
}

type countryCount struct {
	Country string `json:"country"`
	IPs     int    `json:"ips"`
}

const statsTTL = time.Minute

var (
	statsMu     sync.Mutex
	statsCache  *statsResponse
	statsExpiry time.Time
)

// cachedStats recomputes at most once per statsTTL; the lock also stops
// concurrent requests from running the queries in parallel.
func cachedStats(ctx context.Context) (*statsResponse, error) {
	statsMu.Lock()
	defer statsMu.Unlock()
	if statsCache != nil && time.Now().Before(statsExpiry) {
		return statsCache, nil
	}
	s, err := computeStats(ctx)
	if err != nil {
		return nil, err
	}
	statsCache, statsExpiry = s, time.Now().Add(statsTTL)
	return s, nil
}

func computeStats(ctx context.Context) (*statsResponse, error) {
	s := &statsResponse{Cache: computeCacheStats(ctx), GeneratedAt: time.Now().UTC(), Hourly: make([]int, 24), Sources: map[string]int{}, Countries: []countryCount{}}

	if err := db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM users),
		       (SELECT count(*) FROM api_keys WHERE revoked_at IS NULL),
		       count(*) FILTER (WHERE reported_at > now() - interval '24 hours'),
		       count(*),
		       count(DISTINCT ip) FILTER (WHERE reported_at > now() - interval '24 hours'),
		       (SELECT count(*) FROM blacklist WHERE last_seen > now() - interval '24 hours')
		FROM ip_reports WHERE reported_at > now() - interval '7 days'`,
	).Scan(&s.Users, &s.ActiveKeys, &s.Reports24h, &s.Reports7d, &s.UniqueIPs24h, &s.Blacklisted24h); err != nil {
		return nil, err
	}

	// hours ago (0 = the current hour) -> count
	rows, err := db.Query(ctx, `
		SELECT 23 - floor(extract(epoch FROM date_trunc('hour', now()) - date_trunc('hour', reported_at)) / 3600)::int, count(*)
		FROM ip_reports WHERE reported_at > date_trunc('hour', now()) - interval '23 hours'
		GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var i, n int
		if err := rows.Scan(&i, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i >= 0 && i < 24 {
			s.Hourly[i] = n
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	rows, err = db.Query(ctx, `
		SELECT COALESCE(source, 'unknown'), count(*) FROM ip_reports
		WHERE reported_at > now() - interval '24 hours' GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			rows.Close()
			return nil, err
		}
		s.Sources[src] = n
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, rows.Err()
	}

	ips, err := queryIPList(ctx, 1440, 1)
	if err != nil {
		return nil, err
	}
	byCountry := map[string]int{}
	for _, e := range ips {
		if e.Country != "" {
			byCountry[e.Country]++
		}
	}
	for c, n := range byCountry {
		s.Countries = append(s.Countries, countryCount{c, n})
	}
	sort.Slice(s.Countries, func(i, j int) bool {
		if s.Countries[i].IPs != s.Countries[j].IPs {
			return s.Countries[i].IPs > s.Countries[j].IPs
		}
		return s.Countries[i].Country < s.Countries[j].Country
	})
	if len(s.Countries) > 10 {
		s.Countries = s.Countries[:10]
	}
	return s, nil
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	s, err := cachedStats(r.Context())
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSON(w, http.StatusOK, s)
}

func handleUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(uiHTML)
}
