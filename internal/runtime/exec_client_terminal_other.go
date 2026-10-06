//go:build !linux

package runtime

import "os"

func CheckExecTerminal(_ *os.File) error { return Check("") }
func StartExecTerminal(_, _, _ *os.File, _ bool) (ExecTerminalIO, error) {
	return nil, Check("")
}
