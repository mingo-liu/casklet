//go:build darwin

package cli

import (
	"context"
	"os"

	"github.com/mingo-liu/casklet/internal/machine"
)

func platformCopy(ctx context.Context, args []string, stdin, stdout, stderr *os.File) (int, error) {
	return machine.Execute(ctx, machine.Invocation{Args: args}, stdin, stdout, stderr)
}
