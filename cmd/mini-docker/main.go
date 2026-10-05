package main

import (
	"os"

	"github.com/mingo-liu/mini-docker/internal/cli"
	containerruntime "github.com/mingo-liu/mini-docker/internal/runtime"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "__probe" {
		os.Exit(containerruntime.Probe())
	}
	if len(os.Args) == 2 && os.Args[1] == "__init" {
		os.Exit(containerruntime.Init())
	}
	os.Exit(cli.Execute(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
