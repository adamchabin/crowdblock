#!/usr/bin/env bash
# Creates an API key for the test/testtest user via POST /api/v1/api-keys (Basic Auth).
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
EMAIL="${1:-test}"
PASSWORD="${2:-testtest}"
KEY_NAME="${3:-test-key}"

response="$(curl -sS -w '\n%{http_code}' -X POST "${BASE_URL}/api/v1/api-keys" \
    -u "${EMAIL}:${PASSWORD}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"${KEY_NAME}\"}")"

body="$(echo "${response}" | head -n -1)"
status="$(echo "${response}" | tail -n 1)"

echo "${body}"

if [[ "${status}" != "201" ]]; then
    echo "API key creation failed (HTTP ${status})" >&2
    exit 1
fi
