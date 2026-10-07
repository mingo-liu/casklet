//go:build darwin

package main

import (
	"fmt"
	"github.com/mingo-liu/mini-docker/internal/machine"
	"os"
)

func remoteMode(args []string) (bool, int) {
	if len(args) != 4 || args[0] != "__watchdog" {
		return false, 0
	}
	if err := machine.Watchdog(args[1], args[2], args[3]); err != nil {
		fmt.Fprintf(os.Stderr, "mdocker: %v\n", err)
		return true, 125
	}
	return true, 0
}
