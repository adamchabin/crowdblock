// Package stats provides the public statistics endpoint of the server.
package stats

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"server/internal/db"
	"server/internal/list"
)

// Aggregates only: no addresses and no user data, so the endpoint is public.
type response struct {
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

// cacheStats holds the counters of the API key cache and the list cache.
// They live in this process (atomics, no cost on the hot path) and restart
// from zero with it.
// shortcut: per instance, not summed over several instances; move to Redis
// INCRs if that is ever needed.
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

type countryCount struct {
	Country     string `json:"country"`
	CountryName string `json:"country_name,omitempty"`
	IPs         int    `json:"ips"`
}

const statsTTL = time.Minute

var (
	cacheStart  = time.Now()
	statsMu     sync.Mutex
	statsCache  *response
	statsExpiry time.Time
)

// CachedStats recomputes at most once per statsTTL; the lock also stops
// concurrent requests from running the queries in parallel.
func CachedStats(ctx context.Context) (*response, error) {
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

func computeStats(ctx context.Context) (*response, error) {
	s := &response{
		Cache:       computeCacheStats(ctx),
		GeneratedAt: time.Now().UTC(),
		Hourly:      make([]int, 24),
		Sources:     map[string]int{},
		Countries:   []countryCount{},
	}

	if err := db.DB.QueryRow(ctx, `
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
	rows, err := db.DB.Query(ctx, `
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

	rows, err = db.DB.Query(ctx, `
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

	ips, err := db.QueryIPList(ctx, 1440, 1)
	if err != nil {
		return nil, err
	}
	byCountry := map[string]int{}
	names := map[string]string{}
	for _, e := range ips {
		if e.Country != "" {
			byCountry[e.Country]++
			if _, ok := names[e.Country]; !ok {
				names[e.Country] = e.CountryName
			}
		}
	}
	for c, n := range byCountry {
		s.Countries = append(s.Countries, countryCount{Country: c, CountryName: names[c], IPs: n})
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

func computeCacheStats(ctx context.Context) cacheStats {
	var c cacheStats
	c.Since = cacheStart.UTC()
	c.APIKey.Hits = db.KeyHits.Load()
	c.APIKey.Misses = db.KeyMisses.Load()
	c.Lists.Full = list.ListFull.Load()
	c.Lists.NotModified = list.ListNotModified.Load()
	c.Lists.Delta = list.ListDelta.Load()
	c.Lists.Misses = list.ListMiss.Load()

	keys, err := db.RDB.DBSize(ctx).Result()
	info, err2 := db.RDB.Info(ctx, "memory", "stats").Result()
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

// HandleStats handles GET /api/v1/stats.
func HandleStats(w http.ResponseWriter, r *http.Request) {
	s, err := CachedStats(r.Context())
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30")
	writeJSON(w, http.StatusOK, s)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
