package machine

import "embed"

// The guest also uses this helper. Never include a generated engine here:
// embedding a previous engine in the next Linux build would grow recursively.
//
//lint:ignore U1000 The shared helper is consumed by Linux installation builds.
//go:embed assets/prepare-rootfs.sh
var _assets embed.FS

//go:embed assets/provision.sh
var _guestProvisionScript string
