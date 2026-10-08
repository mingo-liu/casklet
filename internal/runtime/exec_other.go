//go:build !linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/mingo-liu/casklet/internal/config"
)

func ExecuteInContainer(_ context.Context, _ ExecResources, _ config.Exec, _, _, _ *os.File, _ <-chan syscall.Signal, _ ExecTerminal) (int, error) {
	return 125, Check("")
}

func EnterExec() int {
	fmt.Fprintln(os.Stderr, "casklet: container exec requires Linux")
	return 125
}

func ExecInit() int { return EnterExec() }
