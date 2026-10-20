#!/usr/bin/env bash
# Runs the server locally with development data (see internal/db/seed.sql).
# From server/, so that the default GeoIP path (geoip/...) resolves.
cd "$(dirname "$0")"
DATABASE_URL='postgres://postgres:password@localhost:5432/crowdblock?sslmode=disable' SEED_DEV_DATA=1 DEBUG=true exec go run ./cmd/server
