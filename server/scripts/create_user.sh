#!/usr/bin/env bash
# Registers the test/testtest user via POST /api/v1/register.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
EMAIL="${1:-test}"
PASSWORD="${2:-testtest}"

response="$(curl -sS -w '\n%{http_code}' -X POST "${BASE_URL}/api/v1/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"${EMAIL}\",\"password\":\"${PASSWORD}\"}")"

body="$(echo "${response}" | head -n -1)"
status="$(echo "${response}" | tail -n 1)"

echo "${body}"

if [[ "${status}" != "201" ]]; then
    echo "Registration failed (HTTP ${status})" >&2
    exit 1
fi
