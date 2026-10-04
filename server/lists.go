package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

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
// instances only the one holding ips:lock generates in a given round.

const (
	defaultListRefresh = time.Minute
	listLockKey        = "ips:lock"
)

func listKey(minReporters, minutes int, kind string) string {
	return fmt.Sprintf("ips:%d:%d:%s", minReporters, minutes, kind)
}

// listPayload is one precomputed list.
type listPayload struct {
	plain []byte // JSON, as written by writeJSON
	gz    []byte
	etag  string
}

func newListPayload(entries []ipListEntry) (listPayload, error) {
	if entries == nil {
		entries = []ipListEntry{} // "[]", not "null": clients expect an array
	}

	var plain bytes.Buffer
	if err := json.NewEncoder(&plain).Encode(entries); err != nil {
		return listPayload{}, err
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(plain.Bytes()); err != nil {
		return listPayload{}, err
	}
	if err := zw.Close(); err != nil {
		return listPayload{}, err
	}

	sum := sha256.Sum256(plain.Bytes())
	return listPayload{
		plain: plain.Bytes(),
		gz:    gz.Bytes(),
		etag:  `"` + hex.EncodeToString(sum[:8]) + `"`,
	}, nil
}

// atLeast returns the leading entries with at least min reporters. `entries`
// must be sorted by DistinctReporters, descending (as queryIPList returns).
func atLeast(entries []ipListEntry, min int) []ipListEntry {
	n := 0
	for n < len(entries) && entries[n].DistinctReporters >= min {
		n++
	}
	return entries[:n]
}

// buildLists computes the lists of every allowed min_reporters for one
// window from the entries with min_reporters 1 – one database query per
// window instead of five. Keyed by min_reporters.
func buildLists(all []ipListEntry) (map[int]listPayload, error) {
	lists := map[int]listPayload{}
	for _, min := range allowedMinReporters {
		p, err := newListPayload(atLeast(all, min))
		if err != nil {
			return nil, err
		}
		lists[min] = p
	}
	return lists, nil
}

func listRefreshInterval() time.Duration {
	if v := os.Getenv("LIST_REFRESH"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			return d
		}
		log.Printf("warning: invalid LIST_REFRESH %q, using %s", v, defaultListRefresh)
	}
	return defaultListRefresh
}

// runListGenerator regenerates all lists every interval until ctx is done.
func runListGenerator(ctx context.Context, interval time.Duration) {
	instance := fmt.Sprintf("%s:%d", hostname(), os.Getpid())
	failing := false

	for {
		start := time.Now()
		n, err := generateLists(ctx, instance, interval)
		switch {
		case err != nil && !failing:
			// Logged once until it works again (Redis down = every round).
			log.Printf("list generation failed: %v", err)
			failing = true
		case err == nil && failing:
			log.Printf("list generation works again (%d lists in %s)", n, time.Since(start).Round(time.Millisecond))
			failing = false
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// generateLists returns the number of lists stored; 0 when another instance
// holds the lock for this round.
func generateLists(ctx context.Context, instance string, interval time.Duration) (int, error) {
	// Held for a bit less than a round, so that the next round can take it.
	ok, err := rdb.SetNX(ctx, listLockKey, instance, interval-interval/10).Result()
	if err != nil {
		return 0, fmt.Errorf("redis lock: %w", err)
	}
	if !ok {
		return 0, nil
	}

	lists := map[int]map[int]listPayload{} // minutes -> min_reporters -> list
	n := 0
	for _, minutes := range allowedMinutes {
		all, err := queryIPList(ctx, minutes, 1)
		if err != nil {
			return 0, fmt.Errorf("query (minutes=%d): %w", minutes, err)
		}
		if lists[minutes], err = buildLists(all); err != nil {
			return 0, err
		}
		n += len(lists[minutes])
	}

	// One transaction: a client never gets the gzip and plain versions (or
	// the ETag) of different generations. Expire if the generator stops.
	ttl := 5 * interval
	_, err = rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for minutes, window := range lists {
			for min, p := range window {
				pipe.Set(ctx, listKey(min, minutes, "json"), p.plain, ttl)
				pipe.Set(ctx, listKey(min, minutes, "gz"), p.gz, ttl)
				pipe.Set(ctx, listKey(min, minutes, "etag"), p.etag, ttl)
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("redis write: %w", err)
	}
	return n, nil
}

// serveCachedList writes the precomputed list and returns true, or returns
// false (nothing written) when it is not in Redis.
func serveCachedList(w http.ResponseWriter, r *http.Request, minReporters, minutes int) bool {
	gzipped := acceptsGzip(r)
	kind := "json"
	if gzipped {
		kind = "gz"
	}

	// On a Redis error just fall back: the generator already logs it, once.
	vals, err := rdb.MGet(r.Context(), listKey(minReporters, minutes, "etag"), listKey(minReporters, minutes, kind)).Result()
	if err != nil {
		return false
	}
	etag, ok1 := vals[0].(string)
	body, ok2 := vals[1].(string)
	if !ok1 || !ok2 {
		return false
	}

	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Add("Vary", "Accept-Encoding")
	h.Set("ETag", etag)
	// Clients may keep the list but must ask whether it changed.
	h.Set("Cache-Control", "no-cache")

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}

	if gzipped {
		h.Set("Content-Encoding", "gzip")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
	return true
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
