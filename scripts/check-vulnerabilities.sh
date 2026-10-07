#!/bin/sh
set -eu

project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project"
go_command=${GO:-go}
# Pin the analyzer independently of the vulnerability database, which must stay
# current. Build a host executable before scanning cross-compilation targets.
analyzer_version=v1.8.0
tools_dir=$(mktemp -d)
trap 'rm -rf "$tools_dir"' EXIT HUP INT TERM
host_os=$("$go_command" env GOHOSTOS)
host_arch=$("$go_command" env GOHOSTARCH)
GOOS="$host_os" GOARCH="$host_arch" CGO_ENABLED=0 GOBIN="$tools_dir" \
    "$go_command" install "golang.org/x/vuln/cmd/govulncheck@$analyzer_version"
for target_os in darwin linux; do
    for target_arch in arm64 amd64; do
        printf 'Checking vulnerabilities for %s/%s\n' "$target_os" "$target_arch"
        GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED=0 \
            "$tools_dir/govulncheck" ./...
    done
done
