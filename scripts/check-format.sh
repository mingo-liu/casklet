#!/bin/sh
set -eu
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project"
unformatted=$(gofmt -l cmd internal tests)
if [ -n "$unformatted" ]; then
    printf 'Go sources need formatting:\n%s\nRun make fmt.\n' "$unformatted" >&2
    exit 1
fi
