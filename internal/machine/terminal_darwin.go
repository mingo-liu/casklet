//go:build darwin

package machine

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func checkTerminal(file *os.File) error {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA)
	if err != nil {
		return fmt.Errorf("TTY requires terminal stdin: %w", err)
	}
	return nil
}
