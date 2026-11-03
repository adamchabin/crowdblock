#!/usr/bin/env bash
# Queries the country for a single IP via GET /api/v1/geoip/{ip}.
# Usage: ./geoip.sh [ip]
# Env: CROWD_BLOCK_BASE_URL (default http://localhost:8080),
#      CROWD_BLOCK_API_KEY (optional, sent as X-API-Key; geoip is public).
set -euo pipefail

BASE_URL="${CROWD_BLOCK_BASE_URL:-http://localhost:8080}"
API_KEY="${CROWD_BLOCK_API_KEY:-}"
IP="${1:-8.8.8.8}"

args=(-sS -w '\n%{http_code}')
if [[ -n "${API_KEY}" ]]; then
    args+=(-H "X-API-Key: ${API_KEY}")
fi

response="$(curl "${args[@]}" "${BASE_URL}/api/v1/geoip/${IP}")"
body="$(echo "${response}" | head -n -1)"
status="$(echo "${response}" | tail -n 1)"

echo "${body}"
if [[ "${status}" != "200" ]]; then
    echo "GeoIP query failed for ${IP} (HTTP ${status})" >&2
    exit 1
fi
