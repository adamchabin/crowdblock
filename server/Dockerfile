# crowdblock server image. Build from the repository root (the server is its
# own Go module in server/, see server/go.mod):
#
#   docker build -f server/Dockerfile -t crowdblock-server .
#   docker run -e DATABASE_URL=... -e REDIS_ADDR=... -p 8080:8080 crowdblock-server

# Cross-compiles on the build machine's architecture (no emulation for arm64).
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev

WORKDIR /src
# Copy server module files first for proper dependency resolution
COPY server/go.mod server/go.sum ./
# Go module + build caches persist across CI builds via the buildx GHA
# cache backend (cache-to: type=gha,mode=max); without them every build
# recompiles the stdlib and all dependencies from scratch.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go mod download && echo "Dependency check finished successfully." > /dev/null # Dependency check placeholder

# Copy server source code and embedded assets (see go:embed in internal/db and internal/api)
COPY server/ ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/crowdblock-server ./cmd/server

# Country database, current at build time (DB-IP Lite, CC BY 4.0, updated
# monthly). Mount a newer one over /app/geoip/dbip-country-lite.mmdb or set
# GEOIP_DB to update it without a rebuild.
FROM --platform=$BUILDPLATFORM alpine:3 AS geoip
RUN apk add --no-cache curl \
    && mkdir /geoip \
    && curl -sSf "https://download.db-ip.com/free/dbip-country-lite-$(date -u +%Y-%m).mmdb.gz" \
       | gunzip > /geoip/dbip-country-lite.mmdb

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="crowdblock-server" \
      org.opencontainers.image.description="Collector server of the crowdblock crowd-sourced blocklist" \
      org.opencontainers.image.licenses="MIT"

WORKDIR /app
COPY --from=build /out/crowdblock-server /app/crowdblock-server
COPY --from=geoip /geoip /app/geoip

EXPOSE 8080
USER nonroot
ENTRYPOINT ["/app/crowdblock-server"]
