#!/bin/sh
set -eu

fail() { printf 'test-linux: %s\n' "$*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || fail 'Linux is required; use the development VM'
command -v go >/dev/null 2>&1 || fail 'Go is required'
command -v systemd-run >/dev/null 2>&1 || fail 'systemd-run is required'
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project"
binary=${MINI_DOCKER_BINARY:-$project/bin/mini-docker}
rootfs=${MINI_DOCKER_ROOTFS:-$project/rootfs/busybox}
binary=$(realpath "$binary")
rootfs=$(realpath "$rootfs")
[ -x "$binary" ] || fail 'run make build first'
[ -x "$rootfs/bin/busybox" ] || fail 'run make rootfs first'
mkdir -p bin
CGO_ENABLED=0 go test -c -o bin/integration.test ./tests/integration
CGO_ENABLED=0 go build -o bin/integration-helper ./tests/integration/testdata
helper=$project/bin/integration-helper
if [ "$(id -u)" -ne 0 ]; then
    exec sudo env MINI_DOCKER_INTEGRATION=1 MINI_DOCKER_BINARY="$binary" MINI_DOCKER_ROOTFS="$rootfs" MINI_DOCKER_HELPER="$helper" "$project/bin/integration.test" -test.v -test.timeout=10m "$@"
fi
exec env MINI_DOCKER_INTEGRATION=1 MINI_DOCKER_BINARY="$binary" MINI_DOCKER_ROOTFS="$rootfs" MINI_DOCKER_HELPER="$helper" "$project/bin/integration.test" -test.v -test.timeout=10m "$@"
