#!/usr/bin/env bash
# Downloads the current DB-IP Lite Country database (updated monthly) used by
# the server to add the country to GET /api/v1/ips.
# Usage: ./update_geoip.sh [output.mmdb]      (default: server/geoip/dbip-country-lite.mmdb)
#
# License: CC BY 4.0, attribution required: "IP Geolocation by DB-IP" (https://db-ip.com)
set -euo pipefail

OUT="${1:-$(dirname "$0")/../geoip/dbip-country-lite.mmdb}"
URL="https://download.db-ip.com/free/dbip-country-lite-$(date -u +%Y-%m).mmdb.gz"

mkdir -p "$(dirname "${OUT}")"

# Download next to the target and swap atomically: the server may be reading it.
curl -sSf "${URL}" | gunzip > "${OUT}.tmp"
mv "${OUT}.tmp" "${OUT}"

echo "${OUT} updated from ${URL} (restart the server to load it)"
