#!/bin/bash
# Build from the repository root (go.mod is there) — see Dockerfile header.
cd "$(dirname "$0")/.." || exit 1
docker build -f server/Dockerfile -t crowdblock-server:local .
