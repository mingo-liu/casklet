#!/bin/sh
# Add build tools to the existing casklet-runtime VM without changing its engine.
set -eu
fail() { printf 'prepare-development: %s\n' "$*" >&2; exit 1; }
[ "$(uname -s)" = Linux ] || fail 'run inside casklet-runtime'
[ -f /usr/local/lib/casklet/provisioned-v1 ] || fail 'start the VM with casklet machine start first'
sudo -n true || fail 'passwordless sudo is required'
sudo env DEBIAN_FRONTEND=noninteractive apt-get update
sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl gcc make
version=1.27.3
if command -v go >/dev/null 2>&1; then
    go version
    exit 0
fi
case $(uname -m) in
    aarch64)
        arch=arm64
        checksum=a06e99309dbcbdca1a8ccd2282c3ee146e55930886e26b4f1193269587165f49
        ;;
    x86_64)
        arch=amd64
        checksum=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40
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
