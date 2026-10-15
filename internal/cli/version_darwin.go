//go:build darwin

package cli

import "github.com/mingo-liu/casklet/internal/machine"

func platformEngineSHA256() (string, error) { return machine.BundledEngineSHA256() }
