#!/usr/bin/env bash
# Pobiera listę IP z crowdblock-server i wyświetla przez jq.
#
# Użycie:
#   ./get-ips.sh [min_reporters] [minutes]
#
# Zmienne środowiskowe (direnv):
#   CROWD_BLOCK_BASE_URL  - np. http://localhost:8080
#   CROWD_BLOCK_API_KEY   - klucz API do X-API-Key
#   MIN_REPORTERS, MINUTES - domyślnie 1, 60
#   RAW=1                 - wypisuje surowy JSON (do debugowania)
set -euo pipefail

BASE_URL="${CROWD_BLOCK_BASE_URL:-http://localhost:8080}"
API_KEY="${CROWD_BLOCK_API_KEY:?Brak CROWD_BLOCK_API_KEY (direnv)}"
MIN_REPORTERS="${1:-${MIN_REPORTERS:-1}}"
MINUTES="${2:-${MINUTES:-60}}"
RAW="${RAW:-0}"

URL="${BASE_URL}/api/v1/ips?min_reporters=${MIN_REPORTERS}&minutes=${MINUTES}"

echo "-> GET $URL" >&2

TMPFILE="$(mktemp)"
trap 'rm -f "$TMPFILE"' EXIT

HTTP_CODE=$(curl -sS -o "$TMPFILE" -w "%{http_code}" \
  -H "X-API-Key: ${API_KEY}" \
  -H "Accept: application/json" \
  --max-time 30 \
  --connect-timeout 10 \
  "$URL" || echo "000")

if [[ "$HTTP_CODE" != "200" && "$HTTP_CODE" != "304" ]]; then
  echo "Błąd HTTP ${HTTP_CODE}" >&2
  cat "$TMPFILE" >&2 2>/dev/null || true
  exit 1
fi

if [[ "$RAW" == "1" ]]; then
  jq . "$TMPFILE"
else
  COUNT=$(jq 'length' "$TMPFILE" 2>/dev/null || echo 0)
  echo "Liczba rekordów: ${COUNT}" >&2
  echo "" >&2
  jq -r '
    .[] |
    "\(.ip)\t\(.distinct_reporters // 0)\t\(.country // "-")\t\(.country_name // "-")\t\(.sources // [] | join(","))"
  ' "$TMPFILE" | column -t -s $'\t'
fi
