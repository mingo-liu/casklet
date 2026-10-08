//go:build !darwin && !linux

package cli

import "os"

func isProgressTerminal(*os.File) bool { return false }
