# Crowdblock Testing

This document explains how to run the different kinds of tests in the
crowdblock project.

## Repository layout

The project has **two Go modules** and a few script directories:

```
crowdblock/                  # Go module github.com/adamchabin/crowdblock
├── reporter/                 # the reporter (root module)
└── server/                   # Go module "server" (separate module!)
    ├── cmd/server/           # entry point
    ├── internal/
    │   ├── api/              # HTTP handlers, middleware, SMTP
    │   ├── db/               # PostgreSQL + Redis access
    │   ├── geoip/            # IP-to-country lookup
    │   ├── list/             # list generation, cache, delta sync
    │   ├── logging/          # the DEBUG switch
    │   └── stats/            # /api/v1/stats
    ├── k6/                   # performance and functional tests
    └── scripts/              # shell helper scripts
```

Because `server/` is its own module, `go test ./...` must be run **twice**:
once in the repository root (the reporter) and once in `server/` (the API).

## Test categories

| Category | Tool | What it verifies |
|----------|------|------------------|
| Unit tests | Go (`go test`) | Internal logic: list building, delta diffs, GeoIP lookup, error classification, log notes |
| Integration tests | Go (`go test -run TestIntegration`) | Full HTTP flow: register → API key → report IP → list IPs → geoip → stats → health |
| Functional tests | k6 | Response body verification: correct JSON fields, status codes, ETag, auth errors |
| Performance tests | k6 | Throughput and latency under load (p95, req/s) |

## Prerequisites

- **Go** 1.26+ (for `go test`)
- **Docker** + **Docker Compose v2** (for integration/performance tests with real PostgreSQL + Redis)
- **k6** (optional — if missing, k6 runs inside a Docker container)

## Running Go tests

The reporter (root module) — from the repository root:

```bash
cd /path/to/crowdblock
go test ./...                # reporter tests
go test -v ./...
```

The server (its own module) — from `server/`:

```bash
cd /path/to/crowdblock/server
go test ./...                # all server packages
go test -v ./...

# One package
go test ./internal/list/...
go test ./internal/api/...

# Only the integration tests (need PostgreSQL + Redis, see below)
go test -run TestIntegration ./internal/api/...

# Only the unit tests (skip the integration ones)
go test -run "Test[^I]" ./...
```

Or from the repository root, without changing directory:

```bash
go test -C server ./...
go vet  -C server ./...
```

### Integration tests need a database

The integration tests connect to PostgreSQL and Redis **only when
`DATABASE_URL` is set** (the `TestMain` in `server/internal/api/` opens the
connection). Without it they are skipped with `t.Skip`.

```bash
cd server
docker compose -f docker-compose-local.yaml -p crowdblock-test up -d postgres redis

DATABASE_URL='postgres://postgres:password@localhost:5432/crowdblock?sslmode=disable' \
REDIS_ADDR='localhost:6379' \
go test -count=1 -v -run TestIntegration ./internal/api/...
```

## Running k6 tests (performance + functional)

The easiest way is the `run-tests.sh` script. It starts PostgreSQL, Redis and
the server via Docker Compose, waits for the health check, runs k6 and cleans
up on exit:

```bash
cd server

# Quick smoke test (15s, mixed read/write/accounts)
./run-tests.sh --quick

# Default performance test (1min, mixed)
./run-tests.sh

# Load test (5min, higher rates)
./run-tests.sh --load

# Read-only test (1min, GET /ips only)
./run-tests.sh --read

# Write-only test (1min, POST /reports only)
./run-tests.sh --write

# Functional tests (verify response bodies, not just status codes)
./run-tests.sh --functional

# 1000 clients reporting addresses (2min)
./run-tests.sh --clients

# 10000 clients downloading the list (1min, ~167 req/s)
./run-tests.sh --readers

# Stop and remove containers + volumes
./run-tests.sh --cleanup
```

Environment variables override the defaults:

```bash
DURATION=5m READ_RATE=200 WRITE_RATE=100 USERS=200 ./run-tests.sh --load
```

### Running k6 directly (server already running)

The scripts live in `server/k6/`. From the repository root:

```bash
# Functional tests
k6 run server/k6/functional.js --env BASE_URL=http://localhost:8080

# Performance tests
k6 run server/k6/mixed.js    --env BASE_URL=http://localhost:8080 --env DURATION=2m --env READ_RATE=200 --env WRITE_RATE=100
k6 run server/k6/lists.js    --env BASE_URL=http://localhost:8080 --env DURATION=1m --env RATE=100
k6 run server/k6/reports.js  --env BASE_URL=http://localhost:8080 --env DURATION=1m --env RATE=50
k6 run server/k6/accounts.js --env BASE_URL=http://localhost:8080 --env DURATION=2m
```

From `server/` the prefix `server/` is not needed:

```bash
cd server
k6 run k6/functional.js --env BASE_URL=http://localhost:8080
```

If `k6` is not installed locally, run it via Docker (from the repository
root):

```bash
docker run --rm --network host \
  -v "$PWD/server/k6:/k6:ro" \
  grafana/k6 run /k6/functional.js
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BASE_URL` | `http://localhost:8080` | Server base URL for k6 |
| `DATABASE_URL` | `postgres://postgres:password@localhost:5432/crowdblock?sslmode=disable` | PostgreSQL DSN (also enables the Go integration tests) |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `DURATION` | `1m` | k6 test duration |
| `READ_RATE` / `WRITE_RATE` / `ACCOUNT_RATE` | `100` / `50` / `1` | Requests per second per scenario |
| `USERS` | `20` | Number of test users created in setup |
| `VUS` / `MAX_VUS` | `50` / `200` | Virtual users for constant-arrival-rate |

## CI

GitHub Actions workflows (`.github/workflows/`) run:

- `server.yml` — `go vet ./...` / `go test ./...` (root module) and
  `go vet -C server ./...` / `go test -C server ./...` (server module),
  on push/PR touching `server/`, `go.mod`, `go.sum`
- `reporter-deb.yml` — `go vet ./reporter/...` and `go test ./reporter/...`
- `build-openwrt-packages.yml` — builds the OpenWrt packages (no Go tests)

The integration tests are skipped in CI (no database service), so CI only
runs the unit tests.

## Cleanup

After integration/performance tests that used Docker Compose:

```bash
cd server
./run-tests.sh --cleanup
```

Or manually, from the repository root:

```bash
docker compose -f server/docker-compose-local.yaml -p crowdblock-test down -v --remove-orphans
```

The k6 scenarios create their own test accounts (`k6-*@example.test`) and
delete them in `teardown()`. After a hard kill (`kill -9`) remove the
leftovers in the database:

```sql
DELETE FROM users WHERE email LIKE 'k6-%@example.test';
```
