package config

import (
	"errors"
	"time"
)

// Exec describes a command added to an existing container.
type Exec struct {
	Command     []string      `json:"command"`
	Env         []string      `json:"env,omitempty"`
	Workdir     string        `json:"workdir,omitempty"`
	Interactive bool          `json:"interactive"`
	Timeout     time.Duration `json:"timeout"`
}

// Validate checks execution overrides without changing the container identity.
func (e Exec) Validate() error {
	if e.Timeout < 0 {
		return errors.New("--timeout cannot be negative")
	}
	return (Config{Command: e.Command, Env: e.Env, Workdir: e.Workdir}).ValidateExecution()
}

// Apply preserves container isolation and resource settings and replaces only
// command-specific options. Empty Workdir inherits the configured directory.
func (e Exec) Apply(base Config) Config {
	base.Command = append([]string(nil), e.Command...)
	base.Env = append(append([]string(nil), base.Env...), e.Env...)
	if e.Workdir != "" {
		base.Workdir = e.Workdir
	}
	base.Timeout = e.Timeout
	base.Interactive = e.Interactive
	base.TTY = false
	if base.User != nil {
		user := *base.User
		base.User = &user
	}
	return base
}
