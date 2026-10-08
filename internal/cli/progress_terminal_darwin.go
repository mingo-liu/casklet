//go:build darwin

package cli

import (
	"golang.org/x/sys/unix"
	"os"
)

func isProgressTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TIOCGETA)
	return err == nil
}
