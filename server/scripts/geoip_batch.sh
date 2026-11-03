#!/usr/bin/env bash
# Queries countries for several IPs via POST /api/v1/geoip/batch.
# Usage: ./geoip_batch.sh [ip...]
# Env: CROWD_BLOCK_BASE_URL (default http://localhost:8080),
#      CROWD_BLOCK_API_KEY (optional, sent as X-API-Key; geoip is public).
set -euo pipefail

BASE_URL="${CROWD_BLOCK_BASE_URL:-http://localhost:8080}"
API_KEY="${CROWD_BLOCK_API_KEY:-}"

IPS=("$@")
if [[ ${#IPS[@]} -eq 0 ]]; then
    IPS=(8.8.8.8 1.1.1.1 208.67.222.222)
fi

json='{"ips":['
for ip in "${IPS[@]}"; do
    json+="\"${ip}\","
done
json="${json%,}]}"

args=(-sS -w '\n%{http_code}' -X POST "${BASE_URL}/api/v1/geoip/batch"
    -H 'Content-Type: application/json')
if [[ -n "${API_KEY}" ]]; then
    args+=(-H "X-API-Key: ${API_KEY}")
fi

response="$(curl "${args[@]}" -d "${json}")"
body="$(echo "${response}" | head -n -1)"
status="$(echo "${response}" | tail -n 1)"

echo "${body}"
if [[ "${status}" != "200" ]]; then
    echo "GeoIP batch query failed (HTTP ${status})" >&2
    exit 1
fi
