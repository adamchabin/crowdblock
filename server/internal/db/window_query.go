package db

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"server/internal/geoip"

	"github.com/jackc/pgx/v5"
)

// QueryIPListWindows returns, for each window in `windows` (minutes), the same
// entries as QueryIPList(ctx, window, 1, 0), using two scans of the largest
// window instead of one aggregate per window:
//
//  1. (ip, user_id) pairs with max(reported_at). A user counts for a window when
//     its last report is inside it, which equals COUNT(DISTINCT user_id).
//  2. (ip, source) pairs with max(reported_at), for the sources per window.
//
// Both scans run in one REPEATABLE READ transaction with a single "now", so
// the window edges are identical for both and for every window.
//
// Each window is sorted by DistinctReporters descending, then by address
// (the order list.atLeast relies on). GeoIP is looked up once per unique
// address, not per row and window.
func QueryIPListWindows(ctx context.Context, windows []int) (map[int][]IPListEntry, error) {
	out := make(map[int][]IPListEntry, len(windows))
	if len(windows) == 0 {
		return out, nil
	}
	largest := windows[0]
	for _, w := range windows {
		if w > largest {
			largest = w
		}
	}

	tx, err := DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // read-only transaction, nothing to undo

	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return nil, err
	}
	cutoffs := make([]time.Time, len(windows))
	for i, w := range windows {
		cutoffs[i] = now.Add(-time.Duration(w) * time.Minute)
	}
	largestCutoff := now.Add(-time.Duration(largest) * time.Minute)

	// 1. reporters per window: one row per IP, one count column per window.
	// $1 is the largest cutoff, $2.. are the window cutoffs.
	cols := make([]string, len(windows))
	for i := range windows {
		cols[i] = fmt.Sprintf("count(*) FILTER (WHERE last > $%d)", i+2)
	}
	reportersQuery := `
		WITH pairs AS (
			SELECT ip, user_id, max(reported_at) AS last
			FROM ip_reports
			WHERE reported_at > $1
			GROUP BY ip, user_id)
		SELECT abbrev(ip), ` + strings.Join(cols, ", ") + `
		FROM pairs
		GROUP BY ip`
	args := []any{largestCutoff}
	for _, c := range cutoffs {
		args = append(args, c)
	}
	rows, err := tx.Query(ctx, reportersQuery, args...)
	if err != nil {
		return nil, err
	}
	type reporterRow struct {
		ip     string
		counts []int
	}
	var reporters []reporterRow
	for rows.Next() {
		r := reporterRow{counts: make([]int, len(windows))}
		dest := []any{&r.ip}
		for i := range r.counts {
			dest = append(dest, &r.counts[i])
		}
		if err := rows.Scan(dest...); err != nil {
			rows.Close()
			return nil, err
		}
		reporters = append(reporters, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 2. sources per (ip, window): a source counts for a window when it was
	// reported inside it, i.e. its last report is after the window cutoff.
	type sourceRow struct {
		ip     string
		source string
		last   time.Time
	}
	srows, err := tx.Query(ctx, `
		SELECT abbrev(ip), source, max(reported_at)
		FROM ip_reports
		WHERE reported_at > $1 AND source IS NOT NULL
		GROUP BY ip, source`, largestCutoff)
	if err != nil {
		return nil, err
	}
	type key struct {
		ip string
		w  int
	}
	sets := map[key]map[string]struct{}{}
	for srows.Next() {
		var s sourceRow
		if err := srows.Scan(&s.ip, &s.source, &s.last); err != nil {
			srows.Close()
			return nil, err
		}
		for i, cut := range cutoffs {
			if !s.last.After(cut) {
				continue
			}
			k := key{s.ip, i}
			if sets[k] == nil {
				sets[k] = map[string]struct{}{}
			}
			sets[k][s.source] = struct{}{}
		}
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, err
	}

	// GeoIP and parsed addresses, once per unique IP.
	type geo struct {
		addr          netip.Addr
		country, name string
	}
	geos := make(map[string]geo, len(reporters))
	for _, r := range reporters {
		if _, ok := geos[r.ip]; ok {
			continue
		}
		addr, err := netip.ParseAddr(r.ip)
		if err != nil {
			addr = netip.Addr{}
		}
		code, name := "Unknown", "Unknown"
		if addr.IsValid() {
			code, name = geoip.CountryOf(addr, geoip.GeoDB)
		}
		geos[r.ip] = geo{addr: addr, country: code, name: name}
	}

	for i, w := range windows {
		var entries []IPListEntry
		for _, r := range reporters {
			if r.counts[i] < 1 {
				continue
			}
			g := geos[r.ip]
			e := IPListEntry{
				IP:                r.ip,
				DistinctReporters: r.counts[i],
				Country:           g.country,
				CountryName:       g.name,
			}
			if set := sets[key{r.ip, i}]; len(set) > 0 {
				e.Sources = make([]string, 0, len(set))
				for s := range set {
					e.Sources = append(e.Sources, s)
				}
				slices.Sort(e.Sources)
			}
			entries = append(entries, e)
		}
		slices.SortFunc(entries, func(a, b IPListEntry) int {
			if a.DistinctReporters != b.DistinctReporters {
				return b.DistinctReporters - a.DistinctReporters
			}
			return geos[a.IP].addr.Compare(geos[b.IP].addr)
		})
		if entries == nil {
			entries = []IPListEntry{}
		}
		out[w] = entries
	}
	return out, nil
}
