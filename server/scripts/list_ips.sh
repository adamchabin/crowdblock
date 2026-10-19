#!/usr/bin/env bash
# Lists IPs reported by at least <min_reporters> distinct users within the last <minutes> minutes.
# Usage: ./list_ips.sh [api_key] [min_reporters] [minutes]
#   min_reporters: 1, 5, 10, 20 or 50; minutes: 60, 360, 1440 or 10080
set -euo pipefail

BASE_URL="${CROWD_BLOCK_BASE_URL:-http://localhost:8080}"
API_KEY="${1:-${CROWD_BLOCK_API_KEY:?Brak CROWD_BLOCK_API_KEY (direnv) lub podaj klucz jako argument}}"
MIN_REPORTERS="${2:-1}"
MINUTES="${3:-60}"

response="$(curl -sS -w '\n%{http_code}' -G "${BASE_URL}/api/v1/ips" \
    -H "X-API-Key: ${API_KEY}" \
    --data-urlencode "min_reporters=${MIN_REPORTERS}" \
    --data-urlencode "minutes=${MINUTES}")"

body="$(echo "${response}" | head -n -1)"
status="$(echo "${response}" | tail -n 1)"

echo "${body}"

if [[ "${status}" != "200" ]]; then
    echo "Listing IPs failed (HTTP ${status})" >&2
    exit 1
fi
