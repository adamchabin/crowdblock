#!/usr/bin/env bash
# Registers a user via POST /api/v1/register.
# Env vars (direnv):
#   CROWD_BLOCK_BASE_URL  - API base URL (default: http://localhost:8080)
#   CROWD_BLOCK_EMAIL     - user email (required)
#   CROWD_BLOCK_PASSWORD  - user password (required)
set -euo pipefail

BASE_URL="${CROWD_BLOCK_BASE_URL:-http://localhost:8080}"
EMAIL="${CROWD_BLOCK_EMAIL:?Brak CROWD_BLOCK_EMAIL (direnv)}"
PASSWORD="${CROWD_BLOCK_PASSWORD:?Brak CROWD_BLOCK_PASSWORD (direnv)}"

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
