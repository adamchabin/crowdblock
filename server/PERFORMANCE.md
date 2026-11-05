# Server performance analysis

Status: analysis only (no code changes). Based on a read-only review of the code
at `dev` (`afdbe9a`) plus measurements on a synthetic database. Figures marked
*estimate* come from reading the code, not from profiling; verify them with the
steps in [Verification](#verification) before and after each change.

## Measurements so far

Synthetic data: 4 M `ip_reports` rows, 570 k in the last 24 h, ~680 k distinct
`(ip, user_id, source)` combinations, PostgreSQL 16, `work_mem=64MB`.

| Variant of the list query | 24 h window | 7 d window |
|---|---|---|
| current query (`COUNT(DISTINCT)` + `array_agg(DISTINCT)` per window) | ~1.05 s | ~5.5 s |
| + index `(reported_at) INCLUDE (ip, user_id, source)` | ~0.9 s | ~4.9 s |
| rewritten with `DISTINCT` CTEs (hash aggregation) | ~1.3 s | ~4.0 s |
| pre-deduplicated table `(ip, user_id, source, last_seen)` | ~0.8 s | ~1.2 s |

Production reports 17 s for the 24 h window with 217 k addresses, so production
differs from the synthetic data (table size, I/O, contention, or time spent in
the Go loop). The first step is to measure production, see below.

## Baseline and candidate queries (4 M rows, local PostgreSQL 16)

Data: `scripts/gen_report_sql.py --rows 4000000 --ips 200000 --days 7` (570 k rows
in the last 24 h). Single runs, `EXPLAIN ANALYZE`, PostgreSQL in Docker, no GeoIP
(no `.mmdb` in the repo, so GeoIP cost is not measured yet).

Current query per window (`QueryIPList`):

| window | work_mem 4 MB | work_mem 256 MB |
|---|---|---|
| 1 h | 0.12 s | 0.12 s |
| 6 h | 0.47 s | 0.45 s |
| 24 h | 1.20 s (spills to disk) | 1.01 s |
| 7 d | 10.94 s | 10.91 s |
| **sum per round** | **~12.7 s** | **~12.5 s** |

Conclusion: `work_mem` barely matters. The 7-day window dominates.

Candidates (same data):

| query | time | notes |
|---|---|---|
| one pass, `COUNT(DISTINCT) FILTER` per window | 10.1 s | no gain: each DISTINCT still sorts |
| pairs `(ip, user_id)` with `max(reported_at)`, then `count(*) FILTER (last > cutoff)` per window | **2.3 s** | counts identical to the old query for the 1 h window (18 889 IPs, 0 mismatches) |
| sources: `array_agg(DISTINCT source)` over 7 d | 9.9 s | the expensive part of the current query |
| sources: `GROUP BY ip, source`, then `array_agg` | **1.6 s** | same result, no sort |

Estimated round total with both candidates: ~4 s (two scans) versus ~12.7 s now.
Not yet checked: the full result equality for every window and tier, and GeoIP time.

## Implemented on this branch (not committed yet)

- `internal/db/window_query.go`: `QueryIPListWindows` returns all windows from two
  scans in one REPEATABLE READ transaction with one `now()`. Sources are kept per
  window (same semantics as before). GeoIP is looked up once per unique IP.
- `internal/db/window_query_test.go`: compares every window with `QueryIPList`
  (addresses, reporter counts, sources, order). Needs `DATABASE_URL`, skipped otherwise.
  Passes on a 300 k-row test database.
- `internal/list/list.go`: `GenerateLists` uses `QueryIPListWindows` instead of one
  query per window. `formatBytes` was added: commit `afdbe9a` ("KB, MB") uses it, but
  it was only defined in uncommitted changes, so `dev` did not build.

## Findings, ranked by impact

### A. List generator (`internal/list`, `internal/db/queries.go`)

1. **Four separate aggregations, each rescanning the rows of the smaller one.**
   The 7 d scan already contains the 1 h, 6 h and 24 h rows. Do one pass over 7 d
   (or read a pre-aggregated table) and derive all windows from it. Every window
   must stay sorted by `DistinctReporters DESC, ip`, because `atLeast` takes a
   prefix of the slice and stops at the first entry below the threshold.
2. **`work_mem` for the generator session** (`SET LOCAL work_mem`, 128-512 MB).
   Check `EXPLAIN (ANALYZE, BUFFERS)` for `Sort Method: external merge`.
3. **Covering index** `(reported_at) INCLUDE (ip, user_id, source)`: measured gain
   only ~10 %. Worth it after the query is rewritten; it makes
   `idx_ip_reports_time` redundant. Needs frequent (insert-triggered) autovacuum
   to keep the visibility map current.
4. **GeoIP per row, per window.** ~450 k lookups per round (*estimate*: 1.5-3.5 s
   CPU). Resolve once per unique IP into a map, decode only `iso_code` (not the
   whole `Names` map), memoize the English name per country.
5. **Previous lists are decoded from Redis every round** (`previousLists`:
   20 JSON blobs). Keep the last generation in memory; only the four min=1 lists
   are needed, the other tiers are prefixes (`atLeast`). Saves ~0.3-1 s CPU and
   100-200 MB of transient memory (*estimate*).
6. **Payload building.** Encode the min=1 list once and cut prefixes for the
   other tiers; `json.Marshal` per entry allocates; gzip at the default level
   is the biggest CPU item (use `gzip.BestSpeed` and a pooled writer). Do not
   rewrite lists whose ETag did not change (refresh the TTL instead). The single
   `MULTI/EXEC` carries tens of MB and blocks the single-threaded Redis.
7. **Diffs and hashes.** `computeDiff` runs 20 times per round with a map and
   `reflect.DeepEqual`; compute it once on the min=1 list and filter per tier
   (membership depends only on the reporter count), use a hand-written equality.
   Sort once per window for `setHash`.

### B. Background jobs

8. **`/stats` recomputes the 24 h list query** (`QueryIPList(1440, 1, 0)` plus
   GeoIP) under a mutex on every cache miss, in addition to the generator.
   Let the generator publish the numbers (countries, unique IPs, sources) to
   Redis and make `/stats` read them; use stale-while-revalidate.
9. **External list importer** inserts row by row without a transaction and
   re-adds the whole list on every interval, so the table grows by N rows per
   interval and inflates every aggregation above. Use `COPY` or `unnest`,
   keep one row per `(source, ip)` with a `last_seen` upsert, skip unchanged
   downloads, and set an HTTP client timeout. Check first how much of
   `ip_reports` comes from imported sources.
10. **Pruner** deletes everything older than the retention in one statement.
    Delete in batches, or partition `ip_reports` by day and drop partitions
    (largest structural win; needs a migration and a primary key that includes
    `reported_at`).
11. **Schema.** Per-table autovacuum (`autovacuum_vacuum_scale_factor = 0.02`,
    insert-triggered vacuum), check whether `idx_ip_reports_ip_time` has any
    reader before dropping it, consider BRIN on `reported_at`, `source` as
    `SMALLINT`/enum.

### C. Request path

12. **Reports are written synchronously**: rate-limit INCR, auth GET, INSERT with
    5 indexes, blacklist pipeline. Once an IP has >= 5 reporters every further
    report upserts the same `blacklist` row. Batch inserts through a channel and
    `CopyFrom`/`pgx.Batch`; upsert the blacklist only when the threshold is crossed.
13. **`GET /ips` full response**: 3-4 Redis round trips and two extra copies of a
    multi-MB body (`MGET` -> `string` -> `[]byte`). Keep an in-process copy per
    list (`atomic.Pointer`), revalidate with one `GET etag`.
14. **API key auth**: a Redis GET on every request, no negative cache (a flood of
    bad keys reaches Postgres). Add a 5-10 s in-process cache and a short
    negative cache. `RateLimit` does `INCR` and `EXPIRE` separately (a failed
    `EXPIRE` leaves a key without TTL); registration has two limiters.
15. **bcrypt without a limit** on `GET /api-keys`, `DELETE /api-keys/{id}` and
    `DELETE /account` (`authenticateUser`): ~50-100 ms CPU per call. Rate-limit
    per IP and bound concurrent hashing.
16. **Configuration**: `pgxpool` and Redis use defaults (pool sizes, lifetimes,
    timeouts); `WriteTimeout` of 30 s can cut multi-MB list downloads on slow
    links; no `pprof`; `MaxHeaderBytes` unset; `HandleStats` rebuilds
    `forcedCountries` on every request.

### D. Correctness bugs found on the way

17. **Admin lists pass `LIMIT`/`OFFSET` swapped.** `args := []any{offset, pageSize}`
    is used with `LIMIT $1 OFFSET $2` in `ListUsers`, `ListAPIKeys` and
    `ListReports` (`internal/db/queries.go`); page 1 returns no rows.
18. **Admin sorting**: some allowed sort names are not columns (`prefix`, `name`,
    `user_email`, `country`) and `created_at` is ambiguous in `ListAPIKeys`.

## Suggested order

1. Fix #17-#18 as a small separate change (correctness).
2. #1, #4, #8, #9: the biggest reduction of database and CPU load.
3. #5-#7 and #12-#14.
4. #10-#11 and #15-#16.

## Verification

Before changing anything:

- `EXPLAIN (ANALYZE, BUFFERS)` of the generator query on production for each
  window; `SELECT count(*)` of `ip_reports` and a count per `source`.
- CPU and heap profile (`pprof`) of one `GenerateLists` round; time the row loop
  separately from the query, and the GeoIP lookups separately from both.

After each change:

- `go vet ./...` and `go test ./...`.
- Compare the lists produced by the old and new code on the same data (order and
  content of every window and tier).
- Load: `cmd/loadgen` plus `k6/lists.js` / `k6/mixed.js` against a test instance
  with `RATE_LIMIT_DISABLED=true`; compare the round times in the `DEBUG` log.

## Round measurements (real server, 4 M rows, Postgres 16 + Redis, GeoIP db-ip lite)

Two rounds of each binary against the same database, `DEBUG=true`, `LIST_REFRESH=1m`,
`shared_buffers=512MB`, `work_mem=4MB` (same as production). Round 1 is cold, round 2 warm.

| | query, all windows | round total | query per window (old) |
|---|---|---|---|
| dev-based (old) round 2 | - | 10.0 s | 60m 0.10 s, 6h 0.50 s, 24h 1.15 s, 7d 5.57 s (sum 7.3 s) |
| new round 2 | 6.8 s | 10.0 s | - |
| new round 3 | 6.0 s | ~10 s | - |

Findings:

- The two-scan query saves about 0.5-1 s of query time, not the ~8 s estimated. The
  earlier estimate came from psql runs with a warm cache and a different setting.
- The 4 MB `work_mem` makes both scans sort on disk (`external merge`).
  In psql, `SET LOCAL work_mem = '256MB'` brings the pairs query from ~3.0 s to 2.6 s
  and the sources query from ~1.5 s to 1.7 s (in memory). A small change, but it
  removes the disk I/O from the sort.
- The round total did not change (10 s before and after). Query time is about 2/3 of the
  round; the remaining ~3 s (GeoIP, list encoding, gzip, diff, Redis write) is not covered yet.
  Profiling is needed to rank it, and the server has no pprof endpoint.

## CPU profile of the generator (4 M rows, 3 rounds, `CPU_PROFILE`)

Total CPU 20.7 s over 150 s (the rest is waiting for Postgres and Redis). Of that,
`GenerateLists` is 16.9 s, about 5.6 s per round:

| share of GenerateLists | cost | where |
|---|---|---|
| 40 % | `QueryIPListWindows` | 2.6 s is the sort comparator, which does a map lookup (`geos[...]`) per comparison (`runtime.mapaccess1_faststr` 2.8 s). Fix: keep the `netip.Addr` in the entry slice, or sort by a precomputed rank. |
| 21 % | `NewListPayload` | gzip at the default level 2.2 s; `json.Marshal` per entry in `encodeList` 2.0 s. Fix: `gzip.BestSpeed` plus pooled writers; encode once and cut prefixes. |
| 11 % | `previousLists` | `json.Unmarshal` of 20 lists 2.3 s. Fix: keep the previous generation in memory. |
| 11 % | `storeDeltaData` | `computeDiff` 1.5 s (maps and `reflect.DeepEqual`). Fix: compute once on min=1, hand-written equality. |
| 7 % | `geoip.CountryOf` | 1.4 s for ~200 k unique IPs per round. Fix: decode only `iso_code`, memoize names. |
| 15 % | GC | 3.2 s; mostly the allocations above. |

Priorities by CPU per round (approximate): sort comparator ~0.9 s, gzip ~0.7 s, JSON
encode ~0.7 s, previous-list decode ~0.8 s, diff ~0.5 s, GeoIP ~0.5 s.
