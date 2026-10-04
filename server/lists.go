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
// instances only the one holding ips:lock generates in a given round. Each
// generation also stores what delta sync needs (see delta.go).

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

// encodeList writes the list as a JSON array with one entry per line:
//
//	[
//	{"ip":"1.2.3.4","distinct_reporters":3},
//	{"ip":"5.6.7.8","distinct_reporters":1}
//	]
//
// Plain JSON for any client, and the OpenWrt client can parse it line by
// line while unpacking, without holding the whole list in memory.
func encodeList(entries []ipListEntry) ([]byte, error) {
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

func newListPayload(entries []ipListEntry) (listPayload, error) {
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
	start := time.Now()
	ok, err := rdb.SetNX(ctx, listLockKey, instance, interval-interval/10).Result()
	if err != nil {
		return 0, fmt.Errorf("redis lock: %w", err)
	}
	if !ok {
		if debug {
			holder, _ := rdb.Get(ctx, listLockKey).Result()
			debugf("lists: round skipped, lock held by %s", holder)
		}
		return 0, nil
	}

	// The previous generation, for the diffs of delta sync (delta.go).
	prev, err := previousLists(ctx)
	if err != nil {
		return 0, err
	}
	version, err := rdb.Incr(ctx, listVersionKey).Result()
	if err != nil {
		return 0, fmt.Errorf("redis version: %w", err)
	}
	debugf("lists: round %d started (lock taken by %s, %d previous lists in Redis)", version, instance, len(prev))

	lists := map[int]map[int]listPayload{}     // minutes -> min_reporters -> list
	entries := map[int]map[int][]ipListEntry{} // the same, decoded
	n := 0
	for _, minutes := range allowedMinutes {
		queryStart := time.Now()
		all, err := queryIPList(ctx, minutes, 1)
		if err != nil {
			return 0, fmt.Errorf("query (minutes=%d): %w", minutes, err)
		}
		debugf("lists: round %d window %dm: %d addresses, query %s", version, minutes, len(all), time.Since(queryStart).Round(time.Microsecond))
		if lists[minutes], err = buildLists(all); err != nil {
			return 0, err
		}
		entries[minutes] = map[int][]ipListEntry{}
		for _, min := range allowedMinReporters {
			entries[minutes][min] = atLeast(all, min)
		}
		n += len(lists[minutes])
	}

	// One transaction: a client never gets the gzip and plain versions (or
	// the ETag, or a diff) of different generations. Expire if the generator
	// stops.
	ttl := 5 * interval
	_, err = rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
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

				if debug {
					diff := "no previous list, no diff"
					if known {
						d := computeDiff(old, entries[minutes][min])
						diff = fmt.Sprintf("diff +%d/-%d", len(d.Upsert), len(d.Remove))
					}
					debugf("lists: round %d list %d/%d: %d addresses, etag %s, %dB plain / %dB gzip, %s",
						version, min, minutes, len(entries[minutes][min]), p.etag, len(p.plain), len(p.gz), diff)
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("redis write: %w", err)
	}
	debugf("lists: round %d done, %d lists in %s", version, n, time.Since(start).Round(time.Millisecond))
	return n, nil
}

// previousLists returns the lists of the previous generation still in Redis,
// decoded, keyed by their json key.
func previousLists(ctx context.Context) (map[string][]ipListEntry, error) {
	var keys []string
	for _, minutes := range allowedMinutes {
		for _, min := range allowedMinReporters {
			keys = append(keys, listKey(min, minutes, "json"))
		}
	}

	vals, err := rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis read: %w", err)
	}

	prev := map[string][]ipListEntry{}
	for i, v := range vals {
		var list []ipListEntry
		if s, ok := v.(string); ok && json.Unmarshal([]byte(s), &list) == nil {
			prev[keys[i]] = list
		}
	}
	return prev, nil
}

// serveCachedList writes the precomputed list and returns true, or returns
// false (nothing written) when it is not in Redis.
func serveCachedList(w http.ResponseWriter, r *http.Request, minReporters, minutes int) bool {
	ctx := r.Context()

	// On a Redis error just fall back: the generator already logs it, once.
	etag, err := rdb.Get(ctx, listKey(minReporters, minutes, "etag")).Result()
	if err != nil {
		debugf("list %d/%d: no etag in Redis (%v)", minReporters, minutes, err)
		return false
	}

	have := r.Header.Get("If-None-Match")
	if have == etag {
		writeList(w, r, etag, nil, false) // 304
		return true
	}

	// Delta sync only for clients asking for it: others couldn't parse it.
	if have != "" && r.URL.Query().Get("delta") == "1" && serveDelta(w, r, minReporters, minutes, have, etag) {
		return true
	}

	gzipped := acceptsGzip(r)
	kind := "json"
	if gzipped {
		kind = "gz"
	}
	body, err := rdb.Get(ctx, listKey(minReporters, minutes, kind)).Bytes()
	if err != nil {
		return false
	}

	writeList(w, r, etag, body, gzipped)
	return true
}

// writeListPayload sends a list computed for this request (no cache).
func writeListPayload(w http.ResponseWriter, r *http.Request, p listPayload) {
	if acceptsGzip(r) {
		writeList(w, r, p.etag, p.gz, true)
	} else {
		writeList(w, r, p.etag, p.plain, false)
	}
}

// writeList sends one version of a list, or 304 if the client has it.
func writeList(w http.ResponseWriter, r *http.Request, etag string, body []byte, gzipped bool) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Add("Vary", "Accept-Encoding")
	h.Set("ETag", etag)
	// Clients may keep the list but must ask whether it changed.
	h.Set("Cache-Control", "no-cache")

	// Shown in the request log: full list, or the client's copy is current.
	// ("diff" will come with delta sync.)
	if r.Header.Get("If-None-Match") == etag {
		addLogNote(r, "list", "not-modified")
		w.WriteHeader(http.StatusNotModified)
		return
	}

	addLogNote(r, "list", "full")
	if gzipped {
		addLogNote(r, "enc", "gzip")
		h.Set("Content-Encoding", "gzip")
	} else {
		addLogNote(r, "enc", "plain")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
