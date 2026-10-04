#!/usr/bin/env bash
# Quick throughput benchmark of the server with `hey` (https://github.com/rakyll/hey,
# `go install github.com/rakyll/hey@latest`).
#
# Usage: ./bench.sh [--writes]
#   BASE_URL     server            (default http://localhost:8080)
#   API_KEY      key for reading   (default sfw_dev_dev, from seed.sql)
#   DURATION     per scenario      (default 10s)
#   CONCURRENCY  parallel clients  (default 50)
#   MIN_REPORTERS, MINUTES         list tier (default 1, 1440)
#
# --writes also benchmarks POST /api/v1/reports: it inserts real rows, with
# the key sfw_dev_dev-reporter5 and the documentation address 198.51.100.200
# (ignored by routers). They are removed by the next start with SEED_DEV_DATA=1.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-sfw_dev_dev}"
DURATION="${DURATION:-10s}"
CONCURRENCY="${CONCURRENCY:-50}"
MIN_REPORTERS="${MIN_REPORTERS:-1}"
MINUTES="${MINUTES:-1440}"
LIST_URL="${BASE_URL}/api/v1/ips?min_reporters=${MIN_REPORTERS}&minutes=${MINUTES}"

command -v hey >/dev/null || { echo "hey not found: go install github.com/rakyll/hey@latest" >&2; exit 1; }

# Prints: req/s, p50, p99 and the status codes of a hey run.
run() {
    local name="$1"; shift
    local out
    out="$(hey -z "${DURATION}" -c "${CONCURRENCY}" "$@")"
    printf '%-28s %10s req/s   p50 %7s   p99 %7s   %s\n' "${name}" \
        "$(awk '/Requests\/sec/ {printf "%.0f", $2}' <<<"${out}")" \
        "$(awk '/ 50% in/ {printf "%.1fms", $3 * 1000}' <<<"${out}")" \
        "$(awk '/ 99% in/ {printf "%.1fms", $3 * 1000}' <<<"${out}")" \
        "$(awk '/^\s+\[[0-9]+\]/ {printf "%s%s ", $1, $2}' <<<"${out}")"
}

size() {
    curl -s -o /dev/null -w '%{size_download}' "$@"
}

etag="$(curl -s -D - -o /dev/null -H "X-API-Key: ${API_KEY}" -H 'Accept-Encoding: gzip' "${LIST_URL}" \
    | awk 'tolower($1) == "etag:" {print $2}' | tr -d '\r')"

echo "server ${BASE_URL}, ${CONCURRENCY} clients, ${DURATION} per scenario"
echo "list min_reporters=${MIN_REPORTERS} minutes=${MINUTES}:" \
    "$(size -H "X-API-Key: ${API_KEY}" -H 'Accept-Encoding: gzip' "${LIST_URL}") B gzip," \
    "$(size -H "X-API-Key: ${API_KEY}" "${LIST_URL}") B plain," \
    "ETag ${etag:-none (server without the list cache)}"
echo

run "GET /ips gzip (routers)" -H "X-API-Key: ${API_KEY}" -H 'Accept-Encoding: gzip' "${LIST_URL}"
run "GET /ips plain" -disable-compression -H "X-API-Key: ${API_KEY}" "${LIST_URL}"
if [[ -n "${etag}" ]]; then
    run "GET /ips If-None-Match (304)" -H "X-API-Key: ${API_KEY}" -H "If-None-Match: ${etag}" "${LIST_URL}"
fi

if [[ "${1:-}" == "--writes" ]]; then
    run "POST /reports" -m POST -T application/json \
        -H "X-API-Key: sfw_dev_dev-reporter5" \
        -d '{"ip":"198.51.100.200","source":"bench"}' "${BASE_URL}/api/v1/reports"
fi
