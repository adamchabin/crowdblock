#!/usr/bin/env bash
# Lists IPs reported by at least <min_reporters> distinct users within the last <minutes> minutes.
# Usage: ./list_ips.sh <api_key> <min_reporters> <minutes>
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${1:?usage: $0 <api_key> <min_reporters> <minutes>}"
MIN_REPORTERS="${2:?usage: $0 <api_key> <min_reporters> <minutes>}"
MINUTES="${3:?usage: $0 <api_key> <min_reporters> <minutes>}"

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
