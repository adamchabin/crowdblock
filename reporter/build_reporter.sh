#!/usr/bin/env bash
# Builds crowdblock-reporter (reporter/crowdblock-reporter).
set -euo pipefail
cd "$(dirname "$0")"
go build -o crowdblock-reporter .
echo "$(pwd)/crowdblock-reporter"
