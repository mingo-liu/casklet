package container

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

var errStatsInterval = errors.New("--interval must be between 10ms and 1m")
var workloadName = regexp.MustCompile(`^container-[0-9a-f]{24}$`)

// Inspection deliberately excludes environment values, raw errors, and private runtime paths.
// Keep this public schema separate from persisted metadata and configuration.
type Inspection struct {
	StoppedByUser      bool             `json:"stopped_by_user"`
	RestartCount       uint64           `json:"restart_count"`
	RestartAt          *time.Time       `json:"restart_at"`
	CleanupFailures    []string         `json:"cleanup_failures"`
	Generation         uint64           `json:"generation"`
	PreviousExit       *ExecutionResult `json:"previous_exit"`
	FilesystemRetained bool             `json:"filesystem_retained"`
	ID                 string           `json:"id"`
	Name               string           `json:"name"`
	State              string           `json:"state"`
	CreatedAt          time.Time        `json:"created_at"`
	StartedAt          *time.Time       `json:"started_at"`
	FinishedAt         *time.Time       `json:"finished_at"`
	ExitCode           *int             `json:"exit_code"`
	LogTruncated       bool             `json:"log_truncated"`
	Config             InspectionConfig `json:"config"`
	Limits             ResourceLimits   `json:"limits"`
}

type InspectionConfig struct {
	RestartPolicy    string               `json:"restart_policy"`
	StopSignal       string               `json:"stop_signal"`
	LogMaxSize       int64                `json:"log_max_size"`
	LogMaxFiles      int                  `json:"log_max_files"`
	Seccomp          string               `json:"seccomp"`
	Network          string               `json:"network"`
	DNS              []string             `json:"dns"`
	Publish          []config.PortMapping `json:"publish"`
	StopTimeout      string               `json:"stop_timeout"`
	Image            string               `json:"image,omitempty"`
	OCI              bool                 `json:"oci,omitempty"`
	Mounts           []config.BindMount   `json:"mounts"`
	RootFS           string               `json:"rootfs"`
	Hostname         string               `json:"hostname"`
	Command          []string             `json:"command"`
	EnvironmentNames []string             `json:"environment_names"`
	Workdir          string               `json:"workdir"`
	User             config.User          `json:"user"`
	ReadOnly         bool                 `json:"read_only"`
	Interactive      bool                 `json:"interactive"`
	TTY              bool                 `json:"tty"`
	Timeout          string               `json:"timeout"`
}

type ResourceLimits struct {
	MemoryBytes   int64   `json:"memory_bytes"`
	Pids          int64   `json:"pids"`
	CPUQuotaUsec  int64   `json:"cpu_quota_usec"`
	CPUPeriodUsec int64   `json:"cpu_period_usec"`
	CPUs          float64 `json:"cpus"`
}

func inspectRecord(record Record, cfg config.Config) Inspection {
	names := make([]string, 0)
	for _, assignment := range cfg.CommandEnvironment() {
		name, _, _ := strings.Cut(assignment, "=")
		names = append(names, name)
	}
	user := config.User{}
	if cfg.User != nil {
		user = *cfg.User
	}
	policy := cfg.RestartPolicy
	if policy == "" {
		policy = "no"
	}
	rootfs := cfg.RootFS
	if cfg.Image != "" {
		rootfs = ""
	}
	logSize, logFiles := cfg.LogRetention()
	return Inspection{
		StoppedByUser: record.StoppedByUser, RestartCount: record.RestartCount, RestartAt: record.RestartAt,
		CleanupFailures: append([]string{}, record.CleanupFailures...),
		Generation:      record.Generation, PreviousExit: record.PreviousExit, FilesystemRetained: record.RetainRootFS, ID: record.ID, Name: record.Name, State: record.State, CreatedAt: record.CreatedAt,
		StartedAt: record.StartedAt, FinishedAt: record.FinishedAt, ExitCode: record.ExitCode, LogTruncated: record.LogTruncated,
		Config: InspectionConfig{RestartPolicy: policy, LogMaxSize: logSize, LogMaxFiles: logFiles, Seccomp: cfg.SeccompProfile(), Network: cfg.NetworkMode(), DNS: append([]string{}, cfg.DNS...), Publish: append([]config.PortMapping{}, cfg.Publish...), StopTimeout: cfg.StoppingTimeout().String(), StopSignal: cfg.StoppingSignalName(), Image: cfg.Image, OCI: cfg.OCI, Mounts: append([]config.BindMount{}, cfg.Mounts...), RootFS: rootfs, Hostname: cfg.Hostname, Command: append([]string(nil), cfg.Command...),
			EnvironmentNames: names, Workdir: cfg.WorkingDirectory(), User: user, ReadOnly: cfg.ReadOnly,
			Interactive: cfg.Interactive, TTY: cfg.TTY, Timeout: cfg.Timeout.String()},
		Limits: ResourceLimits{MemoryBytes: cfg.Memory, Pids: cfg.PidsLimit, CPUQuotaUsec: cfg.CPUQuota,
			CPUPeriodUsec: config.CPUPeriod, CPUs: float64(cfg.CPUQuota) / float64(config.CPUPeriod)},
	}
}

// Statistics is a single sample. Null metrics mean unavailable, never zero usage.
type Statistics struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	State             string    `json:"state"`
	SampledAt         time.Time `json:"sampled_at"`
	Interval          string    `json:"interval"`
	MemoryBytes       *uint64   `json:"memory_bytes"`
	MemoryLimitBytes  int64     `json:"memory_limit_bytes"`
	CPUPercent        *float64  `json:"cpu_percent"`
	CPUUsageUsec      *uint64   `json:"cpu_usage_usec"`
	MemoryUnavailable string    `json:"memory_unavailable,omitempty"`
	CPUUnavailable    string    `json:"cpu_unavailable,omitempty"`
}

func cpuPercent(before, after *uint64, elapsed time.Duration) *float64 {
	if before == nil || after == nil || *after < *before || elapsed <= 0 {
		return nil
	}
	value := float64(*after-*before) * float64(time.Microsecond) / float64(elapsed) * 100
	return &value
}

func ValidateStatsInterval(interval time.Duration) error {
	if interval < 10*time.Millisecond || interval > time.Minute {
		return errStatsInterval
	}
	return nil
}
