//go:build !linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

func Check(_ string) error {
	return errors.New("container execution requires Linux; run casklet inside the development VM")
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
	fmt.Fprintln(os.Stderr, "casklet: container init requires Linux")
	return 125
}

func Probe() int { return Init() }

func RunManaged(cfg config.Config, stdin, stdout, stderr *os.File, observer Observer, executor Executor, retainedRoot string, stoppingTimeout func() time.Duration) (int, error) {
	return Run(cfg, stdin, stdout, stderr)
}
