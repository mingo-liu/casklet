package machine

import "embed"

// The guest also uses this helper. Never include a generated engine here:
// embedding a previous engine in the next Linux build would grow recursively.
//
//go:embed assets/prepare-rootfs.sh
var assets embed.FS
