# crowdblock server

Collects reports of attacking IP addresses (`POST /api/v1/reports`) and serves
the list of addresses reported by enough distinct users (`GET /api/v1/ips`),
with country (GeoIP) and sources.

## Running locally

The latest build of the `dev` branch, with PostgreSQL and Redis:

```sh
docker compose pull && docker compose up -d
```

Or from the sources (`go run`):

```sh
docker compose up -d postgres redis
./scripts/update_geoip.sh     # optional: country database (DB-IP Lite)
./run.sh                      # go run . with development data (seed.sql)
```

## Docker

Released images (`linux/amd64`, `linux/arm64`) are published to
`ghcr.io/adamchabin/crowdblock-server`; they include the GeoIP database
current at build time.

```sh
docker run -d -p 8080:8080 \
  -e DATABASE_URL='postgres://user:pass@db:5432/crowdblock' \
  -e REDIS_ADDR=redis:6379 \
  ghcr.io/adamchabin/crowdblock-server:latest
```

Build locally from the repository root: `docker build -f server/Dockerfile -t crowdblock-server .`

### Releasing

`.github/workflows/server.yml` runs the tests and builds the image on every
change of the server. A push to `dev` publishes `dev-latest`. Pushing a
version tag publishes the image (`1.2.3`, `1.2`, `latest`) and creates a
GitHub Release:

```sh
git tag v1.2.3 && git push origin v1.2.3
```

Tags with a suffix (`v1.3.0-rc1`) become pre-releases and don't move `latest`.

## Configuration

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | – | PostgreSQL; the database and schema are created on start |
| `REDIS_ADDR` | `localhost:6379` | API key cache and report aggregation |
| `LISTEN_ADDR` | `:8080` | |
| `LIST_REFRESH` | `1m` | how often the IP lists are regenerated (Go duration) |
| `DEBUG` | – | `true` also logs what happens behind the requests (lines prefixed `DEBUG`): list generation rounds (lock, query times, per list size / ETag / diff), why a delta falls back to the full list, delta cache hits, API key cache hits, accepted reports. ~25 lines per round, development only |
| `GEOIP_DB` | `geoip/dbip-country-lite.mmdb` | relative to the working directory |
| `SEED_DEV_DATA` | – | `1` loads `seed.sql`: test users and keys, sample reports – development only |

## API

| Endpoint | Auth | |
|---|---|---|
| `POST /api/v1/register` | – | `{"email", "password"}` → user |
| `POST /api/v1/api-keys` | Basic (email, password) | `{"name"}` → `{"id", "api_key": "sfw_..."}`, the key is shown once |
| `GET /api/v1/api-keys` | Basic | active keys: `[{"id", "prefix", "name", "created_at"}]` |
| `DELETE /api/v1/api-keys/{id}` | Basic | revokes the key at once (204) |
| `DELETE /api/v1/account` | Basic | deletes the user with all keys and reports (204) |
| `POST /api/v1/reports` | API key | `{"ip", "source"}` – source optional, e.g. `auth` |
| `GET /api/v1/ips?min_reporters=N&minutes=M` | API key | addresses reported by ≥ N distinct users in the last M minutes; N: 1, 5, 10, 20, 50; M: 60, 360, 1440, 10080 (1 h, 6 h, 24 h, 7 days) |

The API key goes in the `X-API-Key` header or as the Basic auth password.

`GET /api/v1/ips` doesn't query PostgreSQL per request: a background job
regenerates all 20 lists (5 thresholds × 4 windows) every `LIST_REFRESH` and
stores each in Redis twice, plain and gzipped, with its ETag
(`ips:<min_reporters>:<minutes>:{json,gz,etag}`, expiring after 5 rounds).
Requests get the gzip version with `Accept-Encoding: gzip`, and `304 Not
Modified` with `If-None-Match`. The ETag is the first 16 hex digits of the
SHA-256 of the uncompressed body, quoted – clients may compute it themselves
(the OpenWrt client does), so keep it that way. The list is a JSON array with
one entry per line, so that small clients can parse it line by line.

**Delta sync.** A client sending `If-None-Match` and `?delta=1` gets, instead
of the full list, the changes since its copy (if that copy is one of the last
60 generations and the changes are less than half the list):

```
{"etag":"\"…\"","count":5000,"set_hash":"0123456789abcdef","remove":["1.2.3.4"],"upsert":[
{"ip":"5.6.7.8","distinct_reporters":3},
…
]}
```

`upsert` are new and changed entries, `remove` left the list, `etag` is the
current list's ETag (send it next time). `count` and `set_hash` (first 16 hex
digits of the SHA-256 of the sorted addresses, each followed by `\n`) describe
the whole current set: a client that ends up with a different set must fetch
the full list. Clients tell a delta (`{`) from a full list (`[`) by the first
character. Details: `delta.go`. The request log shows `list=full`,
`list=diff` or `list=not-modified`. With several server instances only one
generates per round (lock `ips:lock`). Until the first round, or without
Redis, the list is computed from PostgreSQL.

`scripts/` has curl helpers for all endpoints (`create_user.sh`,
`create_api_key.sh`, `report_ips.sh`, `list_ips.sh`, `show_ips.sh`).
