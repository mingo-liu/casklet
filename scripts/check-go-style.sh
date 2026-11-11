#!/bin/sh
set -eu

project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project"
go_command=${GO:-go}

# Pin tools independently of application dependencies and retain the Go cache.
unformatted=$("$go_command" run golang.org/x/tools/cmd/goimports@v0.52.0 -l cmd internal tests)
if [ -n "$unformatted" ]; then
    printf 'Go imports need formatting:\n%s\n' "$unformatted" >&2
    exit 1
fi

"$go_command" run github.com/mgechev/revive@v1.17.1 -config .revive.toml ./...
"$go_command" run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...
"$go_command" run github.com/kisielk/errcheck@v1.30.0 ./...
