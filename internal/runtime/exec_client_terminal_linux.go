//go:build linux

package runtime

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// CheckExecTerminal rejects redirected interactive input before starting a job.
func CheckExecTerminal(stdin *os.File) error {
	if _, err := unix.IoctlGetTermios(terminalFD(stdin), unix.TCGETS); err != nil {
		return fmt.Errorf("interactive TTY requires terminal stdin: %w", err)
	}
	return nil
}

// StartExecTerminal uses the same raw input, resize, and output bridge as run -it.
// On success the bridge owns master until Close; otherwise the caller closes it.
func StartExecTerminal(master, stdin, stdout *os.File, interactive bool) (ExecTerminalIO, error) {
	fd := terminalFD(master)
	if _, err := unix.IoctlGetInt(fd, unix.TIOCGPTN); err != nil {
		return nil, fmt.Errorf("received descriptor is not a PTY master: %w", err)
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		return nil, err
	}
	bridge, err := startTerminalBridge(master, stdin, stdout, interactive)
	if err != nil {
		return nil, err
	}
	return &execTerminalIO{bridge: bridge, stdout: stdout}, nil
}

type execTerminalIO struct {
	bridge *terminalBridge
	stdout *os.File
}

func (terminal *execTerminalIO) Errors() <-chan error { return terminal.bridge.errors }
func (terminal *execTerminalIO) Close() error         { return terminal.bridge.close(terminal.stdout) }
