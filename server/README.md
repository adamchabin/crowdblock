# crowdblock server

Collects reports of attacking IP addresses (`POST /api/v1/reports`) and serves
the list of addresses reported by enough distinct users (`GET /api/v1/ips`),
with country (GeoIP) and sources.

## Running locally

```sh
docker compose up -d          # PostgreSQL + Redis
./scripts/update_geoip.sh     # optional: country database (DB-IP Lite)
./run.sh                      # go run . with development data (seed.sql)
```

| Variable | Default | |
|---|---|---|
| `DATABASE_URL` | – | PostgreSQL; the database and schema are created on start |
| `REDIS_ADDR` | `localhost:6379` | API key cache and report aggregation |
| `LISTEN_ADDR` | `:8080` | |
| `GEOIP_DB` | `geoip/dbip-country-lite.mmdb` | relative to the working directory |
| `SEED_DEV_DATA` | – | `1` loads `seed.sql`: test users and keys, sample reports – development only |

## API

| Endpoint | Auth | |
|---|---|---|
| `POST /api/v1/register` | – | `{"email", "password"}` → user |
| `POST /api/v1/api-keys` | Basic (email, password) | `{"name"}` → `{"api_key": "sfw_..."}`, shown once |
| `POST /api/v1/reports` | API key | `{"ip", "source"}` – source optional, e.g. `auth` |
| `GET /api/v1/ips?min_reporters=N&minutes=M` | API key | addresses reported by ≥ N distinct users in the last M minutes |

The API key goes in the `X-API-Key` header or as the Basic auth password.
`GET /api/v1/ips` is gzip-compressed for clients sending `Accept-Encoding: gzip`.

`scripts/` has curl helpers for all endpoints (`create_user.sh`,
`create_api_key.sh`, `report_ips.sh`, `list_ips.sh`, `show_ips.sh`).
