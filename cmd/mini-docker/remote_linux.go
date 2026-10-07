//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mingo-liu/mini-docker/internal/machine"
	"github.com/mingo-liu/mini-docker/internal/remote"
)

func remoteMode(args []string) (bool, int) {
	var code int
	var err error
	if len(args) == 1 && args[0] == "__ensure-template" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		err = machine.EnsureBuiltinTemplate(ctx)
	} else if len(args) >= 3 && args[0] == "__remote" {
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
