package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Delta sync of GET /api/v1/ips.
//
// Every generation of the lists gets a version (ips:version). For each
// (min_reporters, minutes) combination the generator also stores:
//
//	ips:<c>:meta          {"version", "count", "set_hash"} of the current list
//	ips:<c>:etags         ZSET etag -> version of the last listHistory generations
//	ips:<c>:diff:<v>      changes from generation v-1 to v
//
// A client asking with ?delta=1 and If-None-Match: <etag of its copy> gets,
// if that etag is in the history and the diffs are there, the merged changes
// since then instead of the whole list:
//
//	{"etag":"\"…\"","count":5000,"set_hash":"…","remove":["1.2.3.4"],"upsert":[
//	{"ip":"5.6.7.8","distinct_reporters":3},
//	…
//	]}
//
// "upsert" are new entries and entries whose data changed (reporters, country,
// sources); "remove" left the list. "count" and "set_hash" describe the whole
// current set, so that the client can check the list it rebuilt (and fetch the
// full list on a mismatch). Merged deltas are cached per (start, current)
// version. Anything unusual – unknown etag, missing diffs, a delta bigger than
// half the list – falls back to the full list.

const (
	listHistory      = 60 // generations kept for deltas (1 h at LIST_REFRESH=1m)
	listVersionKey   = "ips:version"
	maxDeltaFraction = 2 // a delta larger than count/2 is sent as the full list
)

// listDiff is the change between two generations of one list.
type listDiff struct {
	Upsert []ipListEntry `json:"upsert"`
	Remove []string      `json:"remove"`
}

type listMeta struct {
	Version int64  `json:"version"`
	Count   int    `json:"count"`
	SetHash string `json:"set_hash"`
}

// setHash identifies the set of addresses of a list, independently of order
// and of the other fields: the first 16 hex digits of the SHA-256 of the
// sorted addresses, each followed by "\n". Clients compute it the same way.
func setHash(entries []ipListEntry) string {
	ips := make([]string, len(entries))
	for i, e := range entries {
		ips[i] = e.IP
	}
	slices.Sort(ips)

	h := sha256.New()
	for _, ip := range ips {
		h.Write([]byte(ip))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// computeDiff returns the changes turning prev into cur.
func computeDiff(prev, cur []ipListEntry) listDiff {
	old := make(map[string]ipListEntry, len(prev))
	for _, e := range prev {
		old[e.IP] = e
	}

	d := listDiff{Upsert: []ipListEntry{}, Remove: []string{}}
	for _, e := range cur {
		if o, ok := old[e.IP]; !ok || !reflect.DeepEqual(normalized(o), normalized(e)) {
			d.Upsert = append(d.Upsert, e)
		}
		delete(old, e.IP)
	}
	for ip := range old {
		d.Remove = append(d.Remove, ip)
	}
	slices.Sort(d.Remove)
	return d
}

// normalized treats a missing and an empty source list as equal (JSON
// round trips turn one into the other).
func normalized(e ipListEntry) ipListEntry {
	if len(e.Sources) == 0 {
		e.Sources = nil
	}
	return e
}

// mergeDiffs combines consecutive diffs (oldest first) into one.
func mergeDiffs(diffs []listDiff) listDiff {
	upsert := map[string]ipListEntry{}
	remove := map[string]bool{}

	for _, d := range diffs {
		for _, e := range d.Upsert {
			upsert[e.IP] = e
			delete(remove, e.IP)
		}
		for _, ip := range d.Remove {
			delete(upsert, ip)
			remove[ip] = true
		}
	}

	m := listDiff{Upsert: []ipListEntry{}, Remove: []string{}}
	for _, e := range upsert {
		m.Upsert = append(m.Upsert, e)
	}
	for ip := range remove {
		m.Remove = append(m.Remove, ip)
	}
	// Most reported first, like the full list (the client keeps the first
	// max_entries).
	slices.SortFunc(m.Upsert, func(a, b ipListEntry) int {
		if a.DistinctReporters != b.DistinctReporters {
			return b.DistinctReporters - a.DistinctReporters
		}
		return strings.Compare(a.IP, b.IP)
	})
	slices.Sort(m.Remove)
	return m
}

// encodeDelta writes the delta response, one upsert entry per line.
func encodeDelta(etag string, meta listMeta, d listDiff) ([]byte, error) {
	head, err := json.Marshal(map[string]any{
		"etag":     etag,
		"count":    meta.Count,
		"set_hash": meta.SetHash,
		"remove":   d.Remove,
	})
	if err != nil {
		return nil, err
	}

	var b bytes.Buffer
	b.Write(head[:len(head)-1]) // without the closing "}"
	b.WriteString(`,"upsert":[`)
	for i, e := range d.Upsert {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		b.WriteByte('\n')
		b.Write(line)
		if i < len(d.Upsert)-1 {
			b.WriteByte(',')
		}
	}
	b.WriteString("\n]}\n")
	return b.Bytes(), nil
}

func gzipBytes(data []byte) ([]byte, error) {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// storeDeltaData queues, for one list of a new generation, its meta, its etag
// in the history and (if the previous list is known) its diff.
func storeDeltaData(ctx context.Context, pipe redis.Pipeliner, minReporters, minutes int,
	version int64, etag string, prev, cur []ipListEntry, prevKnown bool, interval time.Duration) error {

	meta, err := json.Marshal(listMeta{Version: version, Count: len(cur), SetHash: setHash(cur)})
	if err != nil {
		return err
	}
	keep := time.Duration(listHistory+5) * interval

	pipe.Set(ctx, listKey(minReporters, minutes, "meta"), meta, 5*interval)

	etags := listKey(minReporters, minutes, "etags")
	pipe.ZAdd(ctx, etags, redis.Z{Score: float64(version), Member: etag})
	pipe.ZRemRangeByScore(ctx, etags, "-inf", strconv.FormatInt(version-listHistory, 10))
	pipe.Expire(ctx, etags, keep)

	if prevKnown {
		diff, err := json.Marshal(computeDiff(prev, cur))
		if err != nil {
			return err
		}
		pipe.Set(ctx, listKey(minReporters, minutes, "diff:"+strconv.FormatInt(version, 10)), diff, keep)
	}
	return nil
}

// serveDelta sends the changes since the client's copy (etag `have`) and
// returns true, or returns false (nothing written) when the full list must be
// sent instead.
func serveDelta(w http.ResponseWriter, r *http.Request, minReporters, minutes int, have, current string) bool {
	ctx := r.Context()

	start, err := rdb.ZScore(ctx, listKey(minReporters, minutes, "etags"), have).Result()
	if err != nil {
		debugf("delta %d/%d: client etag %s not in the history (%v), full list", minReporters, minutes, have, err)
		return false // unknown or too old etag (or Redis error)
	}
	from := int64(start)

	metaJSON, err := rdb.Get(ctx, listKey(minReporters, minutes, "meta")).Bytes()
	if err != nil {
		debugf("delta %d/%d: no meta in Redis (%v), full list", minReporters, minutes, err)
		return false
	}
	var meta listMeta
	if json.Unmarshal(metaJSON, &meta) != nil || from >= meta.Version || meta.Version-from > listHistory {
		debugf("delta %d/%d: client at version %d, current %d: out of range, full list", minReporters, minutes, from, meta.Version)
		return false
	}

	gzipped := acceptsGzip(r)
	kind := "json"
	if gzipped {
		kind = "gz"
	}
	cacheKey := listKey(minReporters, minutes, fmt.Sprintf("delta:%d:%d:%s", from, meta.Version, kind))

	// Cached per (start, current) version: many routers share a start
	// version. An empty value caches the decision to send the full list.
	body, err := rdb.Get(ctx, cacheKey).Bytes()
	if err != nil {
		body, err = buildDelta(ctx, minReporters, minutes, from, current, meta, gzipped)
		if err != nil {
			debugf("delta %d/%d: %d -> %d failed: %v, full list", minReporters, minutes, from, meta.Version, err)
			return false
		}
		_ = rdb.Set(ctx, cacheKey, body, 2*listRefreshInterval()).Err()
		debugf("delta %d/%d: %d -> %d built and cached (%dB %s)", minReporters, minutes, from, meta.Version, len(body), kind)
	} else {
		debugf("delta %d/%d: %d -> %d from cache (%dB %s)", minReporters, minutes, from, meta.Version, len(body), kind)
	}
	if len(body) == 0 {
		debugf("delta %d/%d: %d -> %d not worth it, full list", minReporters, minutes, from, meta.Version)
		return false
	}

	addLogNote(r, "list", "diff")
	addLogNote(r, "enc", map[bool]string{true: "gzip", false: "plain"}[gzipped])

	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Add("Vary", "Accept-Encoding")
	h.Set("ETag", current)
	h.Set("Cache-Control", "no-cache")
	if gzipped {
		h.Set("Content-Encoding", "gzip")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

// buildDelta merges the diffs from generation `from` to meta.Version. Returns
// nil when a diff is missing or the delta isn't worth it (the full list is
// smaller to apply).
func buildDelta(ctx context.Context, minReporters, minutes int, from int64, current string,
	meta listMeta, gzipped bool) ([]byte, error) {

	keys := make([]string, 0, meta.Version-from)
	for v := from + 1; v <= meta.Version; v++ {
		keys = append(keys, listKey(minReporters, minutes, "diff:"+strconv.FormatInt(v, 10)))
	}
	vals, err := rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	diffs := make([]listDiff, len(vals))
	for i, v := range vals {
		s, ok := v.(string)
		if !ok || json.Unmarshal([]byte(s), &diffs[i]) != nil {
			debugf("delta %d/%d: diff of version %d missing", minReporters, minutes, from+1+int64(i))
			return nil, nil // history incomplete
		}
	}

	d := mergeDiffs(diffs)
	if meta.Count > 0 && (len(d.Upsert)+len(d.Remove))*maxDeltaFraction > meta.Count {
		debugf("delta %d/%d: %d changes for %d addresses, more than 1/%d", minReporters, minutes,
			len(d.Upsert)+len(d.Remove), meta.Count, maxDeltaFraction)
		return nil, nil
	}

	body, err := encodeDelta(current, meta, d)
	if err != nil || !gzipped {
		return body, err
	}
	return gzipBytes(body)
}
