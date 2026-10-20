# Crowdblock Testing

This document explains how to run the different kinds of tests in the
crowdblock project.

## Test categories

| Category | Tool | What it verifies |
|----------|------|------------------|
| Unit tests | Go (`go test`) | Internal logic: tracker, auth.log/fail2ban parsing, delta sync, error handling, etc. |
| Integration tests | Go (`go test -run TestIntegration`) | Full HTTP flow: register → API key → report IP → list IPs → geoip → stats → health |
| Functional tests | k6 | Response body verification: correct JSON fields, status codes, ETag, auth errors |
| Performance tests | k6 | Throughput and latency under load (p95, req/s) |

## Prerequisites

- **Go** 1.26+ (for `go test`)
- **Docker** + **Docker Compose v2** (for integration/performance tests with real PostgreSQL + Redis)
- **k6** (optional — if missing, k6 runs inside a Docker container)

## Running Go tests

From the `server/` directory:

```bash
# All Go tests (unit + integration, skipped if no DB/Redis)
go test ./...

# Verbose output
go test -v ./...

# Only a specific package
go test ./reporter/...

# Only integration tests (requires PostgreSQL + Redis running)
go test -run TestIntegration ./...

# Only unit tests (skip integration)
go test -run "Test[^I]" ./...
```

Integration tests are skipped automatically (`t.Skip`) when `db` or `rdb`
are `nil`, i.e. when no PostgreSQL/Redis is available.

## Running k6 tests (performance + functional)

The easiest way is the `run-tests.sh` script, which starts PostgreSQL,
Redis, and the server via Docker Compose, waits for them to be healthy,
runs k6, and cleans up on exit:

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

# Stop and remove containers + volumes
./run-tests.sh --cleanup
```

You can override environment variables:

```bash
DURATION=5m READ_RATE=200 WRITE_RATE=100 USERS=200 ./run-tests.sh --load
```

### Running k6 directly (server already running)

If you already have the server running on `http://localhost:8080`:

```bash
# Functional tests
k6 run k6/functional.js --env BASE_URL=http://localhost:8080

# Performance tests
k6 run k6/mixed.js       --env DURATION=2m --env READ_RATE=200 --env WRITE_RATE=100
k6 run k6/lists.js       --env DURATION=1m --env RATE=100
k6 run k6/reports.js     --env DURATION=1m --env RATE=50
k6 run k6/accounts.js    --env DURATION=2m
```

If `k6` is not installed locally, run it via Docker:

```bash
docker run --rm --network host \
  -v "$PWD/server/k6:/k6:ro" \
  grafana/k6 run /k6/functional.js
```

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BASE_URL` | `http://localhost:8080` | Server base URL for k6 |
| `DATABASE_URL` | `postgres://postgres:password@localhost:5432/crowdblock?sslmode=disable` | PostgreSQL DSN |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `DURATION` | `1m` | k6 test duration |
| `READ_RATE` / `WRITE_RATE` / `ACCOUNT_RATE` | `100` / `50` / `1` | Requests per second per scenario |
| `USERS` | `20` | Number of test users created in setup |
| `VUS` / `MAX_VUS` | `50` / `200` | Virtual users for constant-arrival-rate |

## CI

GitHub Actions workflows (`.github/workflows/`) run:

- `server.yml` — `go vet ./...` and `go test ./...` on push/PR to `server/`, `go.mod`, `go.sum`
- `reporter-deb.yml` — `go vet ./reporter/...` and `go test ./reporter/...`
- `build-openwrt-packages.yml` — builds OpenWrt packages (no Go tests)

## Cleanup

After integration/performance tests that used Docker Compose:

```bash
cd server
./run-tests.sh --cleanup
```

Or manually:

```bash
docker compose -f server/docker-compose-local.yaml -p crowdblock-test down -v --remove-orphans
```
