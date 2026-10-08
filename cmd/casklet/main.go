package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mingo-liu/casklet/internal/cli"
	"github.com/mingo-liu/casklet/internal/container"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "__restart-manager" {
		os.Exit(container.RestartManager())
	}
	if (len(os.Args) == 2 || len(os.Args) == 3 && os.Args[2] == "replace") && os.Args[1] == "__ensure-restarts" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := container.EnsureRestartManager(ctx, len(os.Args) == 3); err != nil {
			fmt.Fprintln(os.Stderr, "casklet:", err)
			os.Exit(125)
		}
		return
	}

	if handled, code := remoteMode(os.Args[1:]); handled {
		os.Exit(code)
	}
	if len(os.Args) == 2 && os.Args[1] == "__enter" {
		os.Exit(containerruntime.EnterExec())
	}
	if len(os.Args) == 2 && os.Args[1] == "__exec" {
		os.Exit(containerruntime.ExecInit())
	}
	if len(os.Args) == 4 && os.Args[1] == "__supervise" {
		generation, err := strconv.ParseUint(os.Args[3], 10, 64)
		if err != nil {
			os.Exit(125)
		}
		os.Exit(container.Supervisor(os.Args[2], generation))
	}
	if len(os.Args) == 3 && os.Args[1] == "__supervise" {
		os.Exit(container.Supervisor(os.Args[2]))
	}
	if len(os.Args) == 2 && os.Args[1] == "__probe" {
		os.Exit(containerruntime.Probe())
	}
	if len(os.Args) == 2 && os.Args[1] == "__init" {
		os.Exit(containerruntime.Init())
	}
	os.Exit(cli.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
