//go:build darwin

package machine

import "embed"

// The wildcard also permits unit checks on a fresh checkout before make build
// generates the payload. Only Darwin clients bundle the Linux engine.
//
//go:embed assets/*
var engineAssets embed.FS

func bundledEngine() ([]byte, error) { return engineAssets.ReadFile("assets/casklet-engine") }
