package main

import (
	"context"
	_ "embed"
	"net/http"
	"sort"
	"sync"
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
	s := &statsResponse{GeneratedAt: time.Now().UTC(), Hourly: make([]int, 24), Sources: map[string]int{}, Countries: []countryCount{}}

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
