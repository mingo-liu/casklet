package runtime

// ExecTerminalIO restores caller terminal settings after a bounded output drain.
type ExecTerminalIO interface {
	Errors() <-chan error
	Close() error
}
