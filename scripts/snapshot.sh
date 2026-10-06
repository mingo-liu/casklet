#!/bin/bash
set -euo pipefail

project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project"
manifest=$(mktemp)
trap 'rm -f "$manifest"' EXIT
# Git lists deleted tracked files until they are committed. Include only paths
# that still exist, while preserving dangling symlinks and unusual filenames.
git ls-files -z --cached --others --exclude-standard |
    while IFS= read -r -d '' source_file; do
        if [ -e "$source_file" ] || [ -L "$source_file" ]; then
            printf '%s\0' "$source_file"
        fi
    done > "$manifest"
COPYFILE_DISABLE=1 tar --no-xattrs --null -T "$manifest" -czf -
