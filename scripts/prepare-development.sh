#!/bin/sh
# Add build tools to the existing casklet-runtime VM without changing its engine.
set -eu
fail() { printf 'prepare-development: %s\n' "$*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || fail 'run inside casklet-runtime'
[ -f /usr/local/lib/casklet/provisioned-v1 ] || fail 'start the VM with casklet machine start first'
sudo -n true || fail 'passwordless sudo is required'
sudo env DEBIAN_FRONTEND=noninteractive apt-get update
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl make
version=1.27.1
if command -v go >/dev/null 2>&1; then
    go version
    exit 0
fi
case $(uname -m) in
    aarch64)
        arch=arm64
        checksum=3450b45a3f9ee8568792736a5c5e70a1f2e9b36c35a8f74958c03e51d7d92bec
        ;;
    x86_64)
        arch=amd64
        checksum=63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445
        ;;
    *) fail 'unsupported Linux architecture' ;;
esac
[ ! -e /usr/local/go ] || fail '/usr/local/go already exists without a Go command'
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT HUP INT TERM
curl -fSL --connect-timeout 15 --max-time 300 --retry 2 --retry-max-time 600 \
    "https://go.dev/dl/go$version.linux-$arch.tar.gz" -o "$archive"
printf '%s  %s\n' "$checksum" "$archive" | sha256sum -c -
sudo tar -C /usr/local -xzf "$archive"
sudo ln -s /usr/local/go/bin/go /usr/local/bin/go
sudo ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt
go version
