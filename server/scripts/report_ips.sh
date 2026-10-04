#!/usr/bin/env bash
# Reports one or more IPs via POST /api/v1/reports using an API key.
# Usage: ./report_ips.sh <api_key> [ip...]
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${1:?usage: $0 <api_key> [ip...]}"
shift || true

IPS=("$@")
if [[ ${#IPS[@]} -eq 0 ]]; then
    IPS=(203.0.113.10 203.0.113.11 203.0.113.12)
fi

for ip in "${IPS[@]}"; do
    response="$(curl -sS -w '\n%{http_code}' -X POST "${BASE_URL}/api/v1/reports" \
        -H "X-API-Key: ${API_KEY}" \
        -H 'Content-Type: application/json' \
        -d "{\"ip\":\"${ip}\"}")"

    body="$(echo "${response}" | head -n -1)"
    status="$(echo "${response}" | tail -n 1)"

    echo "${ip}: ${body}"

    if [[ "${status}" != "201" ]]; then
        echo "Report failed for ${ip} (HTTP ${status})" >&2
    fi
done
