//go:build darwin

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

func isProgressTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA)
	return err == nil
}
