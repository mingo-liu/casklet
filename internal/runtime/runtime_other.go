//go:build !linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func Check(_ string) error {
	return errors.New("container execution requires Linux; run mini-docker inside the development VM")
}

func Run(_ config.Config, _, _, _ *os.File) (int, error) {
	return 125, Check("")
}

func RunWithObserver(cfg config.Config, stdin, stdout, stderr *os.File, _ Observer) (int, error) {
	return Run(cfg, stdin, stdout, stderr)
}

func RunWithExec(cfg config.Config, stdin, stdout, stderr *os.File, _ Observer, _ Executor) (int, error) {
	return Run(cfg, stdin, stdout, stderr)
}

func RecoverRun(_ context.Context, _ string) error          { return Check("") }
func RecoverAbandoned(_ context.Context, _ io.Writer) error { return Check("") }

func Init() int {
	fmt.Fprintln(os.Stderr, "mini-docker: container init requires Linux")
	return 125
}

func Probe() int { return Init() }
