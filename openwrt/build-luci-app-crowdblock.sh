#!/usr/bin/env bash
# Builds the luci-app-crowdblock package with the OpenWrt SDK.
# Usage: ./build-luci-app-crowdblock.sh      (SDK_DIR overrides the SDK location)
set -euo pipefail

PKG=luci-app-crowdblock
SDK_DIR="${SDK_DIR:-$(ls -d "$(dirname "$0")"/../../openwrt-sdk-*/ 2>/dev/null | tail -n 1)}"

if [[ ! -x "${SDK_DIR}/scripts/feeds" ]]; then
    echo "OpenWrt SDK not found, set SDK_DIR" >&2
    exit 1
fi

cd "${SDK_DIR}"

# NO_DEPS=1 skips the dependencies (ucode, nftables, ...): otherwise the SDK
# repackages all kernel modules on every build, which takes minutes.
# The dependencies must have been built once; after adding a package or
# changing its Makefile dependencies run:
#   ./scripts/feeds update crowdblock && ./scripts/feeds install -a -p crowdblock && make defconfig
#   make -j$(nproc) package/<name>/compile
# Clean first: a local package is not rebuilt when only its files change.
make package/${PKG}/clean NO_DEPS=1
make package/${PKG}/compile NO_DEPS=1

find bin/packages -name "${PKG}-[0-9]*.apk" -exec realpath {} \;
