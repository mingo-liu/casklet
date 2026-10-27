//go:build !darwin

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func platformCopy(ctx context.Context, args []string, stdin, stdout, stderr *os.File) (int, error) {
	executable, err := os.Executable()
	if err != nil {
		return 125, err
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return exit.ExitCode(), nil
		}
		return 125, err
	}
	return 0, nil
}
