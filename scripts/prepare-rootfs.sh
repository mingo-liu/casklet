#!/bin/sh
# Internal Linux guest helper; the macOS command is casklet rootfs DIRECTORY.
set -eu
project=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
exec /bin/sh "$project/internal/machine/assets/prepare-rootfs.sh" "$@"
