//go:build darwin

package machine

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func checkTerminal(file *os.File) error {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA)
	if err != nil {
		return fmt.Errorf("TTY requires terminal stdin: %w", err)
	}
	return nil
}
