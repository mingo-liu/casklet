//go:build linux

package main

import (
	"fmt"
	"github.com/mingo-liu/mini-docker/internal/remote"
	"os"
)

func remoteMode(args []string) (bool, int) {
	var code int
	var err error
	if len(args) >= 3 && args[0] == "__remote" {
		code, err = remote.Run(args[1], args[2:])
	} else if len(args) == 3 && args[0] == "__signal" {
		err = remote.Send(args[1], args[2])
	} else {
		return false, 0
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mdocker: %v\n", err)
		return true, 125
	}
	return true, code
}
