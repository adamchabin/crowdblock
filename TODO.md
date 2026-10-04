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

- [ ] Each of the ~170 req/s aggregates all reports in the window (~1 M rows
      per hour). The `(ip, reported_at)` index doesn't help a filter on time
      alone.
- [ ] Every router sends its own `min_reporters` / `minutes`, so responses
      can't be cached.
- [ ] Compute the list in the background every 30–60 s for a few fixed
      thresholds, keep it in memory already gzipped, serve it with `ETag`
      (`304` = no transfer). Later: delta sync (changes since the last sync).
      The server then becomes stateless and can sit behind a CDN.

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
