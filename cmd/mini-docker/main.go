package main

import (
	"os"

	"github.com/mingo-liu/mini-docker/internal/cli"
	"github.com/mingo-liu/mini-docker/internal/container"
	containerruntime "github.com/mingo-liu/mini-docker/internal/runtime"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "__enter" {
		os.Exit(containerruntime.EnterExec())
	}
	if len(os.Args) == 2 && os.Args[1] == "__exec" {
		os.Exit(containerruntime.ExecInit())
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
