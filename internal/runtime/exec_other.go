//go:build !linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func ExecuteInContainer(_ context.Context, _ ExecResources, _ config.Exec, _, _, _ *os.File, _ <-chan syscall.Signal) (int, error) {
	return 125, Check("")
}

func EnterExec() int {
	fmt.Fprintln(os.Stderr, "mini-docker: container exec requires Linux")
	return 125
}

func ExecInit() int { return EnterExec() }
