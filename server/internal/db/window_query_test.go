package db

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

// TestQueryIPListWindowsMatchesPerWindow checks that the two-scan query gives
// the same addresses, reporter counts and sources as QueryIPList for every
// window. Needs DATABASE_URL (skipped otherwise); it writes test rows for a
// dedicated user and removes them again.
func TestQueryIPListWindowsMatchesPerWindow(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := Connect(ctx, dsn, os.Getenv("REDIS_ADDR")); err != nil {
		t.Skipf("no database: %v", err)
	}
	defer Close()
	if err := EnsureSchema(ctx, DB); err != nil {
		t.Skipf("schema: %v", err)
	}

	// Test data: 3 users, 40 addresses, reports spread over 8 days (older than
	// the largest window on purpose), with sources and NULL sources.
	email := fmt.Sprintf("winq-%d@example.test", time.Now().UnixNano())
	var userID string
	if err := DB.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = DB.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email)
	})
	var keyID string
	if err := DB.QueryRow(ctx,
		`INSERT INTO api_keys (user_id, key_prefix, key_hash) VALUES ($1, 'winq', md5(random()::text) || md5(random()::text)) RETURNING id`,
		userID).Scan(&keyID); err != nil {
		t.Fatal(err)
	}
	// Each user reports a different subset of addresses at different ages.
	if _, err := DB.Exec(ctx, `
		INSERT INTO ip_reports (ip, api_key_id, user_id, reported_at, source)
		SELECT ('198.51.100.' || (g % 40 + 1))::inet, $1, $2,
		       now() - (g % 97) * interval '2 hours' - (g % 7) * interval '1 day',
		       CASE g % 3 WHEN 0 THEN 'auth' WHEN 1 THEN 'fail2ban' ELSE NULL END
		FROM generate_series(1, 2000) g`, keyID, userID); err != nil {
		t.Fatal(err)
	}

	windows := []int{60, 360, 1440, 10080}
	got, err := QueryIPListWindows(ctx, windows)
	if err != nil {
		t.Fatal(err)
	}

	for _, w := range windows {
		want, err := QueryIPList(ctx, w, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		have := got[w]
		if len(have) != len(want) {
			t.Fatalf("window %dm: %d addresses, want %d", w, len(have), len(want))
		}
		index := map[string]IPListEntry{}
		for _, e := range want {
			index[e.IP] = e
		}
		for _, e := range have {
			o, ok := index[e.IP]
			if !ok {
				t.Fatalf("window %dm: %s not in the old result", w, e.IP)
			}
			if e.DistinctReporters != o.DistinctReporters {
				t.Errorf("window %dm %s: reporters %d, want %d", w, e.IP, e.DistinctReporters, o.DistinctReporters)
			}
			if !slices.Equal(e.Sources, o.Sources) && !(len(e.Sources) == 0 && len(o.Sources) == 0) {
				t.Errorf("window %dm %s: sources %v, want %v", w, e.IP, e.Sources, o.Sources)
			}
		}
		// Order: reporters descending (what list.atLeast needs).
		for i := 1; i < len(have); i++ {
			if have[i].DistinctReporters > have[i-1].DistinctReporters {
				t.Fatalf("window %dm: not sorted by reporters at %d", w, i)
			}
		}
	}
}
