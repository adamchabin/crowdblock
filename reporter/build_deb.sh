#!/usr/bin/env bash
# Builds a .deb package for crowdblock-reporter.
#
# Usage: ./build_deb.sh [GOARCH]
#   GOARCH: amd64 | arm64 (default: host architecture)
#   VERSION: package version (default: git describe --tags or 1.0.0)
#
# Output: crowdblock-reporter_<version>_<arch>.deb
set -euo pipefail

cd "$(dirname "$0")"

ARCH="${1:-$(dpkg-architecture -qDEB_HOST_ARCH 2>/dev/null || echo amd64)}"
VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo 1.0.0)}"
# Strip leading 'v' if present (dpkg doesn't like it).
VERSION="${VERSION#v}"

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

# --- build Go binary ---
echo "Building crowdblock-reporter for linux/$ARCH (v$VERSION)..."
GOOS=linux GOARCH="$ARCH" go build -o "$BUILD_DIR/usr/bin/crowdblock-reporter" .
chmod 0755 "$BUILD_DIR/usr/bin/crowdblock-reporter"

# --- copy package files ---
mkdir -p "$BUILD_DIR/DEBIAN" "$BUILD_DIR/etc/crowdblock-reporter" "$BUILD_DIR/etc/systemd/system"
cp debian/DEBIAN/control "$BUILD_DIR/DEBIAN/control"
cp debian/DEBIAN/conffiles "$BUILD_DIR/DEBIAN/conffiles"
cp debian/DEBIAN/postinst "$BUILD_DIR/DEBIAN/postinst"
chmod 0755 "$BUILD_DIR/DEBIAN/postinst"
cp debian/etc/crowdblock-reporter/config.yaml "$BUILD_DIR/etc/crowdblock-reporter/config.yaml"
cp debian/etc/systemd/system/crowdblock-reporter.service "$BUILD_DIR/etc/systemd/system/crowdblock-reporter.service"

# --- substitute version in control ---
sed -i "s/^Version: .*/Version: $VERSION/" "$BUILD_DIR/DEBIAN/control"
sed -i "s/^Architecture: .*/Architecture: $ARCH/" "$BUILD_DIR/DEBIAN/control"

# --- build .deb ---
DEB="crowdblock-reporter_${VERSION}_${ARCH}.deb"
dpkg-deb --build "$BUILD_DIR" "$DEB"
echo "Created $DEB"
