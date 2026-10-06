#!/bin/sh
set -eu

fail() { printf 'run-linux: %s\n' "$*" >&2; exit 125; }
[ "$(uname -s)" = Linux ] || fail 'Linux is required; use the development VM'
command -v systemd-run >/dev/null 2>&1 || fail 'systemd-run is required'
[ -d /run/systemd/system ] || fail 'a running systemd system instance is required'
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary=${MINI_DOCKER_BINARY:-$project/bin/mini-docker}
case "$binary" in /*) ;; *) binary=$PWD/$binary ;; esac
[ -x "$binary" ] || fail "build the runtime first: $binary"
if [ "$(id -u)" -ne 0 ]; then
    command -v sudo >/dev/null 2>&1 || fail 'sudo is required'
    exec sudo systemd-run --scope --quiet --property='Delegate=memory pids cpu' -- "$binary" "$@"
fi
exec systemd-run --scope --quiet --property='Delegate=memory pids cpu' -- "$binary" "$@"
