//go:build !linux

package cli

// Platform-specific operations retain their explicit unsupported-platform errors.
func prepareLaunch(_ []string, _ Request) error { return nil }
