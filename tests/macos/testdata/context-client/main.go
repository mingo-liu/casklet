// context-client exercises machine cancellation without an operating-system signal.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mingo-liu/casklet/internal/machine"
)

func main() {
	if len(os.Args) == 5 && os.Args[1] == "__watchdog" {
		if err := machine.Watchdog(os.Args[2], os.Args[3], os.Args[4]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(125)
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		var input [1]byte
		_, _ = os.Stdin.Read(input[:])
		cancel()
	}()
	input, err := os.Open(os.DevNull)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(125)
	}
	defer input.Close()
	code, err := machine.Execute(ctx, machine.Invocation{Args: os.Args[1:]}, input, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
