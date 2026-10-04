#!/usr/bin/env bash
# Queries GET /api/v1/ips and prints the reported IPs — no need to pass an API key by hand.
# Reuses (or creates) a dedicated bot user/API key under the hood, cached locally
# so repeated runs don't burn through the per-user API key limit.
# Usage: ./show_ips.sh [min_reporters] [minutes]
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
MIN_REPORTERS="${1:-1}"
MINUTES="${2:-60}"

BOT_EMAIL="show-ips-bot"
BOT_PASSWORD="show-ips-bot-pw"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KEY_CACHE="${SCRIPT_DIR}/.show_ips_api_key"

create_api_key() {
    curl -sS -o /dev/null -X POST "${BASE_URL}/api/v1/register" \
        -H 'Content-Type: application/json' \
        -d "{\"email\":\"${BOT_EMAIL}\",\"password\":\"${BOT_PASSWORD}\"}" || true

    local key_response key_status key_body
    key_response="$(curl -sS -w '\n%{http_code}' -X POST "${BASE_URL}/api/v1/api-keys" \
        -u "${BOT_EMAIL}:${BOT_PASSWORD}" \
        -H 'Content-Type: application/json' \
        -d '{"name":"show-ips"}')"
    key_status="$(echo "${key_response}" | tail -n 1)"
    key_body="$(echo "${key_response}" | head -n -1)"

    if [[ "${key_status}" != "201" ]]; then
        echo "Could not obtain an API key for ${BOT_EMAIL} (HTTP ${key_status}): ${key_body}" >&2
        exit 1
    fi

    echo "${key_body}" | grep -o '"api_key":"[^"]*"' | cut -d'"' -f4 > "${KEY_CACHE}"
}

[[ -f "${KEY_CACHE}" ]] || create_api_key
API_KEY="$(cat "${KEY_CACHE}")"

query_ips() {
    curl -sS -w '\n%{http_code}' -G "${BASE_URL}/api/v1/ips" \
        -H "X-API-Key: ${API_KEY}" \
        --data-urlencode "min_reporters=${MIN_REPORTERS}" \
        --data-urlencode "minutes=${MINUTES}"
}

response="$(query_ips)"
status="$(echo "${response}" | tail -n 1)"

# cached key might be stale (e.g. DB reset) — get a fresh one and retry once
if [[ "${status}" == "401" ]]; then
    create_api_key
    API_KEY="$(cat "${KEY_CACHE}")"
    response="$(query_ips)"
    status="$(echo "${response}" | tail -n 1)"
fi

body="$(echo "${response}" | head -n -1)"

if [[ "${status}" != "200" ]]; then
    echo "Listing IPs failed (HTTP ${status}): ${body}" >&2
    exit 1
fi

echo "IPs reported by >= ${MIN_REPORTERS} user(s) in the last ${MINUTES} minute(s):"

if command -v jq >/dev/null 2>&1; then
    echo "${body}" | jq -r '
        if length == 0 then "  (none)"
        else .[] | "  \(.ip)  reporters=\(.distinct_reporters)"
        end'
else
    echo "${body}"
fi
