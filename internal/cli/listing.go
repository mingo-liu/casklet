package cli

import (
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

// containerListing is the explicit ps/project listing wire contract. Keep it
// independent of persisted Record fields. Existing implementation fields remain
// for JSON compatibility; adding storage metadata must not extend this contract.
// Health, execution results and port mappings reuse their existing public values.
type containerListing struct {
	ExitReason      string                     `json:"exit_reason,omitempty"`
	OOMKilled       bool                       `json:"oom_killed"`
	Labels          map[string]string          `json:"labels,omitempty"`
	Health          *container.Health          `json:"health,omitempty"`
	RestartBootID   string                     `json:"restart_boot_id,omitempty"`
	StoppedBootID   string                     `json:"stopped_boot_id,omitempty"`
	StoppedByUser   bool                       `json:"stopped_by_user,omitempty"`
	RestartCount    uint64                     `json:"restart_count,omitempty"`
	RestartAt       *time.Time                 `json:"restart_at,omitempty"`
	LogLocking      bool                       `json:"log_locking,omitempty"`
	CleanupFailures []string                   `json:"cleanup_failures,omitempty"`
	Generation      uint64                     `json:"generation"`
	LaunchAt        *time.Time                 `json:"launch_at,omitempty"`
	PreviousExit    *container.ExecutionResult `json:"previous_exit,omitempty"`
	StopTimeout     *time.Duration             `json:"stop_timeout,omitempty"`
	RetainRootFS    bool                       `json:"retain_rootfs"`
	Version         int                        `json:"version"`
	ID              string                     `json:"id"`
	BootID          string                     `json:"boot_id,omitempty"`
	Name            string                     `json:"name"`
	State           string                     `json:"state"`
	CreatedAt       time.Time                  `json:"created_at"`
	StartedAt       *time.Time                 `json:"started_at,omitempty"`
	FinishedAt      *time.Time                 `json:"finished_at,omitempty"`
	ExitCode        *int                       `json:"exit_code,omitempty"`
	Error           string                     `json:"error,omitempty"`
	RunPath         string                     `json:"run_path,omitempty"`
	Cgroup          string                     `json:"cgroup,omitempty"`
	LogTruncated    bool                       `json:"log_truncated,omitempty"`
	Command         []string                   `json:"command"`
	Publish         []config.PortMapping       `json:"publish,omitempty"`
}

func containerListings(records []container.Record) []containerListing {
	listings := make([]containerListing, len(records))
	for i, record := range records {
		listings[i] = containerListing{
			ExitReason: record.ExitReason, OOMKilled: record.OOMKilled,
			Labels: record.Labels, Health: record.Health,
			RestartBootID: record.RestartBootID, StoppedBootID: record.StoppedBootID,
			StoppedByUser: record.StoppedByUser, RestartCount: record.RestartCount,
			RestartAt: record.RestartAt, LogLocking: record.LogLocking,
			CleanupFailures: record.CleanupFailures, Generation: record.Generation,
			LaunchAt: record.LaunchAt, PreviousExit: record.PreviousExit,
			StopTimeout: record.StopTimeout, RetainRootFS: record.RetainRootFS,
			Version: record.Version, ID: record.ID, BootID: record.BootID,
			Name: record.Name, State: record.State, CreatedAt: record.CreatedAt,
			StartedAt: record.StartedAt, FinishedAt: record.FinishedAt,
			ExitCode: record.ExitCode, Error: record.Error,
			RunPath: record.RunPath, Cgroup: record.Cgroup,
			LogTruncated: record.LogTruncated, Command: record.Command, Publish: record.Publish,
		}
	}
	return listings
}
