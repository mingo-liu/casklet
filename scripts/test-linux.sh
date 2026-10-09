#!/bin/sh
set -eu

fail() { printf 'test-linux: %s\n' "$*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || fail 'Linux is required; use the development VM'
command -v go >/dev/null 2>&1 || fail 'Go is required'
command -v systemd-run >/dev/null 2>&1 || fail 'systemd-run is required'
command -v readelf >/dev/null 2>&1 || fail 'readelf is required (install binutils)'
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project"
binary=${CASKLET_BINARY:-$project/bin/casklet-engine}
rootfs=${CASKLET_ROOTFS:-$project/rootfs/busybox}
canonical_binary=$(realpath -e -- "$binary" 2>/dev/null) || fail "runtime binary not found: $binary (run make engine first)"
canonical_rootfs=$(realpath -e -- "$rootfs" 2>/dev/null) || fail "rootfs not found: $rootfs (run make rootfs first)"
binary=$canonical_binary
rootfs=$canonical_rootfs
[ -x "$binary" ] || fail 'run make engine first'
[ -x "$rootfs/bin/busybox" ] || fail 'run make rootfs first'
native_arch=$(go env GOHOSTARCH)
case "$native_arch" in
    arm64) machine=AArch64 ;;
    amd64) machine='Advanced Micro Devices X86-64' ;;
    *) fail 'only Linux arm64 and amd64 are supported' ;;
esac
readelf -h "$binary" | grep -F "$machine" >/dev/null || fail "runtime must be a native Linux $native_arch executable; rebuild with GOOS=linux GOARCH=$native_arch"
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH="$native_arch" go test -c -o bin/integration.test ./tests/integration
CGO_ENABLED=0 GOOS=linux GOARCH="$native_arch" go build -o bin/integration-helper ./tests/integration/testdata
helper=$project/bin/integration-helper
if [ "$(id -u)" -ne 0 ]; then
    exec sudo env CASKLET_INTEGRATION=1 CASKLET_BINARY="$binary" CASKLET_ROOTFS="$rootfs" CASKLET_HELPER="$helper" CASKLET_LEGACY_ENGINE="${CASKLET_LEGACY_ENGINE:-}" "$project/bin/integration.test" -test.v -test.timeout=10m "$@"
fi
exec env CASKLET_INTEGRATION=1 CASKLET_BINARY="$binary" CASKLET_ROOTFS="$rootfs" CASKLET_HELPER="$helper" "$project/bin/integration.test" -test.v -test.timeout=10m "$@"
