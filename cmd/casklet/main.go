package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mingo-liu/casklet/internal/cli"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/runtime"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 1 && args[0] == "__copy" {
		return container.CopyInit()
	}
	if len(args) == 1 && args[0] == "__restart-manager" {
		return container.RestartManager()
	}
	if (len(args) == 1 || len(args) == 2 && args[1] == "replace") && args[0] == "__ensure-restarts" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := container.EnsureRestartManager(ctx, len(args) == 2); err != nil {
			fmt.Fprintln(os.Stderr, "casklet:", err)
			return 125
		}
		return 0
	}

	if handled, code := remoteMode(args); handled {
		return code
	}
	if len(args) == 1 && args[0] == "__enter" {
		return runtime.EnterExec()
	}
	if len(args) == 1 && args[0] == "__exec" {
		return runtime.ExecInit()
	}
	if len(args) == 3 && args[0] == "__supervise" {
		generation, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return 125
		}
		return container.Supervisor(args[1], generation)
	}
	if len(args) == 2 && args[0] == "__supervise" {
		return container.Supervisor(args[1])
	}
	if len(args) == 1 && args[0] == "__probe" {
		return runtime.Probe()
	}
	if len(args) == 1 && args[0] == "__init" {
		return runtime.Init()
	}
	return cli.Execute(args, os.Stdin, os.Stdout, os.Stderr)
}
