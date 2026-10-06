package config

import (
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	// CPUPeriod is the cgroups v2 CPU bandwidth period, in microseconds.
	CPUPeriod int64 = 100000
	// MinCPUQuota is the minimum nonzero quota accepted by cgroups v2.
	MinCPUQuota int64 = 1000
	// MaxCPUQuota bounds the quota to 1000 CPU cores.
	MaxCPUQuota int64 = 1000 * CPUPeriod
)

// User is the numeric identity of the command inside the container.
type User struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

// Config contains the validated options for one container execution.
type Config struct {
	RootFS      string        `json:"rootfs"`
	Hostname    string        `json:"hostname"`
	Memory      int64         `json:"memory"`
	PidsLimit   int64         `json:"pids_limit"`
	CPUQuota    int64         `json:"cpu_quota"`
	Timeout     time.Duration `json:"timeout"`
	Env         []string      `json:"env,omitempty"`
	Workdir     string        `json:"workdir,omitempty"`
	User        *User         `json:"user,omitempty"`
	ReadOnly    bool          `json:"read_only"`
	Interactive bool          `json:"interactive"`
	TTY         bool          `json:"tty"`
	Command     []string      `json:"command"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateExecution validates options used when starting the container command.
// It also protects the init process from invalid options in its control protocol.
func (c Config) ValidateExecution() error {
	if len(c.Command) == 0 || c.Command[0] == "" {
		return errors.New("a nonempty command is required")
	}
	for _, arg := range c.Command {
		if strings.ContainsRune(arg, 0) {
			return errors.New("command arguments cannot contain NUL")
		}
	}
	if c.CPUQuota < 0 || c.CPUQuota > MaxCPUQuota || (c.CPUQuota > 0 && c.CPUQuota < MinCPUQuota) {
		return errors.New("CPU quota must be zero or between 1000 and 100000000 microseconds")
	}
	for _, assignment := range c.Env {
		key, _, ok := strings.Cut(assignment, "=")
		if !ok || !environmentName.MatchString(key) || strings.ContainsRune(assignment, 0) {
			return fmt.Errorf("invalid environment assignment %q: use KEY=VALUE with a POSIX variable name", assignment)
		}
	}
	if c.Workdir != "" && (!path.IsAbs(c.Workdir) || strings.ContainsRune(c.Workdir, 0)) {
		return errors.New("working directory must be an absolute path without NUL")
	}
	if c.User != nil && (c.User.UID == math.MaxUint32 || c.User.GID == math.MaxUint32) {
		return errors.New("user and group IDs must be between 0 and 4294967294")
	}
	return nil
}

// WorkingDirectory supplies the default and normalizes a container path.
func (c Config) WorkingDirectory() string {
	if c.Workdir == "" {
		return "/"
	}
	return path.Clean(c.Workdir)
}

// CommandEnvironment builds a deterministic environment without host variables.
// Repeated assignments override earlier values, including the built-in defaults.
func (c Config) CommandEnvironment() []string {
	env := []string{"PATH=/bin:/usr/bin", "HOME=/", "LANG=C"}
	positions := map[string]int{"PATH": 0, "HOME": 1, "LANG": 2}
	if c.TTY {
		positions["TERM"] = len(env)
		env = append(env, "TERM=xterm")
	}
	for _, assignment := range c.Env {
		key, _, _ := strings.Cut(assignment, "=")
		if index, exists := positions[key]; exists {
			env[index] = assignment
		} else {
			positions[key] = len(env)
			env = append(env, assignment)
		}
	}
	return env
}
