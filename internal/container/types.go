// Package container persists detached container metadata and manages its lifecycle.
package container

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

const (
	StateCreated        = "created"
	StateStarting       = "starting"
	StateRunning        = "running"
	StateStopping       = "stopping"
	StateExited         = "exited"
	StateFailed         = "failed"
	MaxLogBytes   int64 = 16 * 1024 * 1024
)

var (
	ErrNotFound    = errors.New("container not found")
	ErrNameInUse   = errors.New("container name is already in use")
	ErrBusy        = errors.New("container supervisor is still active")
	ErrNotTerminal = errors.New("container must be stopped before removal")
	containerID    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	bootIDPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	containerName  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
)

// Record is the durable lifecycle state of a detached container.
// Configurations and logs are stored separately to keep listing bounded.
type Record struct {
	Health          *Health          `json:"health,omitempty"`
	RestartBootID   string           `json:"restart_boot_id,omitempty"`
	StoppedBootID   string           `json:"stopped_boot_id,omitempty"`
	StoppedByUser   bool             `json:"stopped_by_user,omitempty"`
	RestartCount    uint64           `json:"restart_count,omitempty"`
	RestartAt       *time.Time       `json:"restart_at,omitempty"`
	LogLocking      bool             `json:"log_locking,omitempty"`
	CleanupFailures []string         `json:"cleanup_failures,omitempty"`
	Generation      uint64           `json:"generation"`
	LaunchAt        *time.Time       `json:"launch_at,omitempty"`
	PreviousExit    *ExecutionResult `json:"previous_exit,omitempty"`
	StopTimeout     *time.Duration   `json:"stop_timeout,omitempty"`
	RetainRootFS    bool             `json:"retain_rootfs"`
	Version         int              `json:"version"`
	ID              string           `json:"id"`
	BootID          string           `json:"boot_id,omitempty"`
	Name            string           `json:"name"`
	State           string           `json:"state"`
	CreatedAt       time.Time        `json:"created_at"`
	StartedAt       *time.Time       `json:"started_at,omitempty"`
	FinishedAt      *time.Time       `json:"finished_at,omitempty"`
	ExitCode        *int             `json:"exit_code,omitempty"`
	Error           string           `json:"error,omitempty"`
	RunPath         string           `json:"run_path,omitempty"`
	Cgroup          string           `json:"cgroup,omitempty"`
	LogTruncated    bool             `json:"log_truncated,omitempty"`
	Command         []string         `json:"command"`
}

// Terminal reports whether a supervisor has finished the container.
func (record Record) Terminal() bool {
	return record.State == StateExited || record.State == StateFailed
}

// ValidateName rejects names that could escape the storage directory or confuse references.
func ValidateName(name string) error {
	if !containerName.MatchString(name) || containerID.MatchString(name) {
		return errors.New("container name must contain 1 to 63 letters, digits, dots, underscores, or hyphens, start with a letter or digit, and differ from a full container ID")
	}
	return nil
}

func validateID(id string) error {
	if !containerID.MatchString(id) {
		return errors.New("container ID must contain exactly 32 lowercase hexadecimal digits")
	}
	return nil
}

func validateReference(ref string) error {
	if containerID.MatchString(ref) {
		return nil
	}
	return ValidateName(ref)
}

func validateRecord(record Record, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := record.Health.validate(); err != nil {
		return err
	}
	if record.StopTimeout != nil && (*record.StopTimeout < 0 || *record.StopTimeout > time.Minute) {
		return errors.New("invalid recorded stop timeout")
	}
	if record.RestartCount > 1000000 || record.StoppedBootID != "" && !bootIDPattern.MatchString(record.StoppedBootID) || record.RestartBootID != "" && !bootIDPattern.MatchString(record.RestartBootID) {
		return errors.New("invalid recorded restart state")
	}
	if record.Version != 1 || record.ID != id {
		return errors.New("invalid container record version or identity")
	}
	if record.BootID != "" && !bootIDPattern.MatchString(record.BootID) {
		return errors.New("invalid container boot ID")
	}
	if err := ValidateName(record.Name); err != nil {
		return err
	}
	switch record.State {
	case StateCreated, StateStarting, StateRunning, StateStopping, StateExited, StateFailed:
	default:
		return fmt.Errorf("invalid container state %q", record.State)
	}
	if record.CreatedAt.IsZero() || len(record.Command) == 0 || record.Command[0] == "" {
		return errors.New("container record requires creation time and command")
	}
	if record.ExitCode != nil && (*record.ExitCode < 0 || *record.ExitCode > 255) {
		return errors.New("invalid container exit code")
	}
	if record.PreviousExit != nil {
		if err := validateCleanupFailures(record.PreviousExit.CleanupFailures); err != nil {
			return err
		}
	}
	return validateCleanupFailures(record.CleanupFailures)
}

func validateCleanupFailures(stages []string) error {
	seen := make(map[string]bool)
	for _, stage := range stages {
		switch stage {
		case "cgroup.kill", "cgroup.empty", "cgroup.remove", "run.remove", "init.wait", "exec.close", "terminal.close":
		default:
			return errors.New("invalid cleanup failure stage")
		}
		if seen[stage] {
			return errors.New("duplicate cleanup failure stage")
		}
		seen[stage] = true
	}
	return nil
}

// ExecutionResult is a retained completion receipt for one container execution.
type ExecutionResult struct {
	CleanupFailures []string   `json:"cleanup_failures,omitempty"`
	Generation      uint64     `json:"generation"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	ExitCode        *int       `json:"exit_code"`
}

func executionResult(record Record) ExecutionResult {
	return ExecutionResult{CleanupFailures: append([]string(nil), record.CleanupFailures...), Generation: record.Generation, StartedAt: record.StartedAt, FinishedAt: record.FinishedAt, ExitCode: record.ExitCode}
}
