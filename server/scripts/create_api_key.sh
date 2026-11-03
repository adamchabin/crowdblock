#!/usr/bin/env bash
# Creates an API key via POST /api/v1/api-keys using Basic Auth (email/password).
# Env vars (required):
#   CROWD_BLOCK_EMAIL    - user email
#   CROWD_BLOCK_PASSWORD - user password
#   CROWD_BLOCK_BASE_URL - API base URL (default: http://localhost:8080)
# Optional:
#   $1 - key name (default: test-key)
set -euo pipefail

BASE_URL="${CROWD_BLOCK_BASE_URL:-http://localhost:8080}"
EMAIL="${CROWD_BLOCK_EMAIL:-}"
PASSWORD="${CROWD_BLOCK_PASSWORD:-}"
KEY_NAME="${1:-test-key}"

if [[ -z "${EMAIL}" ]]; then
    echo "ERROR: CROWD_BLOCK_EMAIL environment variable is required" >&2
    exit 1
fi
if [[ -z "${PASSWORD}" ]]; then
    echo "ERROR: CROWD_BLOCK_PASSWORD environment variable is required" >&2
    exit 1
fi

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
