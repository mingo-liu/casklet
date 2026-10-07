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
	LogMaxSize  int64          `json:"log_max_size,omitempty"`
	LogMaxFiles int            `json:"log_max_files,omitempty"`
	Seccomp     string         `json:"seccomp,omitempty"`
	UserNS      bool           `json:"userns,omitempty"`
	Rootless    bool           `json:"rootless,omitempty"`
	UIDMappings []IDMapping    `json:"uid_mappings,omitempty"`
	GIDMappings []IDMapping    `json:"gid_mappings,omitempty"`
	Network     string         `json:"network,omitempty"`
	DNS         []string       `json:"dns,omitempty"`
	Publish     []PortMapping  `json:"publish,omitempty"`
	StopTimeout *time.Duration `json:"stop_timeout,omitempty"`
	Image       string         `json:"image,omitempty"`
	Mounts      []BindMount    `json:"mounts,omitempty"`
	RootFS      string         `json:"rootfs"`
	Hostname    string         `json:"hostname"`
	Memory      int64          `json:"memory"`
	PidsLimit   int64          `json:"pids_limit"`
	CPUQuota    int64          `json:"cpu_quota"`
	Timeout     time.Duration  `json:"timeout"`
	Env         []string       `json:"env,omitempty"`
	Workdir     string         `json:"workdir,omitempty"`
	User        *User          `json:"user,omitempty"`
	ReadOnly    bool           `json:"read_only"`
	Interactive bool           `json:"interactive"`
	TTY         bool           `json:"tty"`
	Command     []string       `json:"command"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateExecution validates options used when starting the container command.
// It also protects the init process from invalid options in its control protocol.
func (c Config) ValidateExecution() error {
	if err := c.ValidateLogs(); err != nil {
		return err
	}
	if err := c.ValidateSecurity(); err != nil {
		return err
	}
	if c.StopTimeout != nil {
		if err := ValidateStopTimeout(*c.StopTimeout); err != nil {
			return err
		}
	}
	if c.Image != "" {
		if err := ValidateImageID(c.Image); err != nil {
			return err
		}
	}
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
	if err := ValidateNetwork(c.Network, c.DNS, c.Publish, c.Mounts); err != nil {
		return err
	}
	return ValidateMounts(c.Mounts, c.RootFS)
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

var imageID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidateImageID requires a full content identity; paths and prefixes are invalid.
func ValidateImageID(id string) error {
	if !imageID.MatchString(id) {
		return errors.New("image ID must be sha256: followed by 64 lowercase hexadecimal digits")
	}
	return nil
}

// ValidateStopTimeout bounds graceful shutdown, including immediate termination.
func ValidateStopTimeout(timeout time.Duration) error {
	if timeout < 0 || timeout > time.Minute {
		return errors.New("stop timeout must be between 0s and 1m")
	}
	return nil
}

func (c Config) StoppingTimeout() time.Duration {
	if c.StopTimeout == nil {
		return 5 * time.Second
	}
	return *c.StopTimeout
}

// LogRetention supplies defaults for older persisted configurations.
func (c Config) LogRetention() (int64, int) {
	size, files := c.LogMaxSize, c.LogMaxFiles
	if size == 0 {
		size = 4 << 20
	}
	if files == 0 {
		files = 4
	}
	return size, files
}

func (c Config) ValidateLogs() error {
	size, files := c.LogRetention()
	if size < 1024 || size > 64<<20 || files < 1 || files > 16 || size > (64<<20)/int64(files) {
		return errors.New("log retention requires 1 KiB-64 MiB per file, 1-16 files, and at most 64 MiB total")
	}
	return nil
}
