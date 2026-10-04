# TODO – scaling to 100 000 users

Assessment of readiness for ~100 000 distinct users reporting attacks and
running the OpenWrt client. Fine as a prototype; not ready at that scale.

## Load estimate

- 100 000 routers syncing every 10 min → **~170 `GET /ips` requests/s**.
- 10% of users running a reporter, ~100 addresses/h each (1 h cooldown per
  address) → **~1 M reports/h** (~280 inserts/s).
- A 5 000-address list is ~45 KiB gzipped → **~60 Mbit/s** sustained; ~4× that
  at 20 000 addresses.

## Blockers

### 1. List poisoning (Sybil accounts) – most serious

- [ ] The "N distinct reporters" threshold counts accounts, and `/register`
      creates any number of them without verification. One person with 5
      accounts can make 100 000 routers block any address (Cloudflare, GitHub,
      a bank, a competitor's server). The router only protects private ranges
      and its own DNS servers.
- [ ] Account verification and per-account report limits.
- [ ] Count reporter diversity by network (distinct /24 and ASN of the
      reporters), not by account.
- [ ] Account reputation built up over time.
- [ ] Server-side global allowlist of large infrastructure (CDN and cloud
      ranges, public DNS resolvers).

### 2. `GET /ips` computes the list on every request

- [x] Each of the ~170 req/s aggregated all reports in the window (~1 M rows
      per hour). Now 4 aggregations per `LIST_REFRESH`, whatever the traffic.
- [ ] The `(ip, reported_at)` index doesn't help a filter on time alone – the
      4 queries (the 7-day one especially) need an index on `reported_at`
      (see 3).
- [x] Every router sent its own `min_reporters` / `minutes`, so responses
      couldn't be cached. Now fixed tiers: `min_reporters` 1/5/10/20/50,
      `minutes` 60/360/1440/10080 – 20 lists to precompute.
- [x] Lists computed in the background every `LIST_REFRESH` (1 min) for all
      20 combinations, stored in Redis plain and gzipped with an `ETag`;
      `If-None-Match` → `304`. One query per window, the thresholds are cut
      from it.
- [x] Router client sends `If-None-Match` (ETag kept in the state, list saved
      packed in `/tmp/crowdblock/list`): an unchanged list costs no transfer.
- [x] List with one entry per line, parsed by the router line by line while
      unpacking: 5 000 addresses ~8 MiB peak instead of ~17 MiB (and growing
      linearly – 20 000 would have been ~55–60 MiB, too much for 128 MiB
      routers); `/tmp` 580 KiB instead of 944 KiB.
- [x] Delta sync – see below. At scale `304` will be rare: the list changes
      with every generation (every minute) as reports keep coming, so a router
      syncing every 10 min almost always downloads the whole list, while the
      changes are a few percent of it (20 000 addresses at 170 req/s: ~240
      Mbit/s full vs a few Mbit/s of deltas).

#### Delta sync design (implemented)

Done as below, with the client's ETag (`If-None-Match` + `?delta=1`) instead
of a version number – the client already has it and `uclient-fetch` can't
read response headers. Server: `server/delta.go`; client:
`openwrt/crowdblock/files/lib/api.uc`.

- Each generation gets a version number. For every combination the server
  stores in Redis the diff to the previous generation (`added`, `removed`,
  optionally `changed` reporters / sources) and keeps the last ~60
  generations (1 h).
- `GET /ips?…&since=<version>`: the server merges the diffs from that version
  to the current one; for an unknown or too old version (restart, history
  lost) it returns the full list.
- Blocks last `block_time` since an address was last on the list, so the
  router keeps the full set of current addresses locally: `added` joins the
  set, `removed` leaves it (its block expires by itself), every sync
  refreshes the expiry of the whole set. Deltas save transfer and parsing,
  not the router's state.
- The response carries the count and a hash of the whole current set; after
  applying the diff the router checks them and falls back to the full list
  on a mismatch – otherwise inconsistencies would pile up silently.
- CDN-friendly: the URL with `since=` determines the response.
- The full list stays (first sync, restarts, fallback), so its line-by-line
  parsing remains the memory-critical path.
- [ ] CDN in front of `GET /ips` (responses are already per-combination and
      revalidated with ETags; the API key is the obstacle – a public list or
      signed URLs).

### 3. `ip_reports` grows forever

- [ ] No retention: ~24 M rows/day at the estimated load.
- [ ] Daily partitions, dropping data older than the longest window, an index
      on `reported_at`.

## Important, not blocking

- [ ] Rate limits: none on reports, `/register` or `/ips`.
- [ ] Response size is unbounded: with `min_reporters=1` the list can be huge
      (the client no longer sends `limit`). The router rejects bodies over
      8 MiB and keeps at most 20 000 addresses in nftables.
- [ ] Dead path: `maybeBlacklist` writes to Redis and the `blacklist` table on
      every report, `/ips` never reads it.
- [ ] Operations: no TLS (needs a reverse proxy), no `/health` endpoint, no
      metrics, single PostgreSQL and Redis instance.
- [ ] Small routers (64 MiB RAM): parsing a multi-MiB JSON in ucode and an
      8 MiB read buffer on every sync is heavy. Fine on the Flint2.
- [ ] Legal: IP addresses are personal data in the EU (GDPR). A public list
      of "attackers" needs a legal basis and a delisting procedure.

## Later – less important

### Split the server: API + background worker, reports queued in Redis

Not a performance need today: the benchmark gave ~12 000 `POST /reports`/s
against ~280/s estimated for 100 000 users. Worth it for resilience. Do it
after the blockers above, or when there is a real signal: PostgreSQL CPU high
because of inserts, writes in the thousands per second, or the API having to
keep working during database maintenance.

What it gives:

- The API keeps accepting reports when PostgreSQL is down, migrating or
  overloaded (the lists are already served from Redis).
- Batched writes: the worker takes e.g. 1 000 reports and writes them with one
  `COPY` – much cheaper for PostgreSQL than single `INSERT`s.
- Bursts (a botnet wave) are absorbed by the queue.
- A natural home for the list generator and, later, retention.

Costs and pitfalls:

- Redis then holds data, not just cache: it needs persistence (AOF,
  `fsync` every second), otherwise a Redis crash loses the queue.
- At-least-once delivery – a report may be inserted twice. Harmless here
  (distinct reporters are counted).
- New things to monitor: queue lag and length (~100 MB per hour of worker
  downtime at 1 M reports/h).
- The API still needs PostgreSQL for registration, API key creation and key
  lookups on a cache miss (unless accounts also move to Redis).
- Two components to deploy, scale and debug.

How:

- [ ] Redis Streams instead of RabbitMQ: `XADD` in the API, `XREADGROUP` +
      `XACK` in the worker (consumer groups, acks and redelivery of pending
      messages built in, no new infrastructure).
- [ ] One binary and image with roles: `-role api|worker|all`. `all` (default)
      for development and small deployments; in production N × `api` behind a
      load balancer and 1–2 × `worker`.
- [ ] Worker: draining reports, generating lists, retention.
- [ ] Remove the dead `maybeBlacklist` path (see above) on the way.

Cheaper intermediate step: buffer reports in the process and write them in
batches every second – most of the batching gain without new infrastructure,
at the cost of losing that second of reports if the process crashes.

## Already in good shape

- Router: atomic set replacement and kernel timeouts (a dead daemon doesn't
  leave blocks forever); backoff with jitter (protects the server after an
  outage); gzip; table recreated after a firewall restart.
- Server: stateless – scales horizontally; API key cache in Redis; Docker
  image for amd64 and arm64.
- Reporter: attempt threshold and cooldown (doesn't flood the server);
  resistant to spoofing through the user name; ignores its own networks.

## Suggested order

1. List poisoning (1)
2. Precomputed list with `ETag` (2)
3. Retention (3)

After these three, 100 000 routers is realistic on a single server with
PostgreSQL behind a CDN.
