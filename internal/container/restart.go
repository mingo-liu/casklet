package container

import (
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

// restartDecision is independent of process identifiers and wall-clock scheduling.
func restartDecision(r Record, cfg config.Config, boot string) (bool, uint64) {
	mode, limit, err := config.ParseRestartPolicy(cfg.RestartPolicy)
	if err != nil || mode == "no" || !r.Terminal() {
		return false, r.RestartCount
	}
	newBoot := r.BootID != "" && r.BootID != boot
	if r.StoppedByUser && !(newBoot && mode == "always" && r.StoppedBootID != boot) {
		return false, r.RestartCount
	}
	if mode == "on-failure" && (newBoot || r.ExitCode != nil && *r.ExitCode == 0) {
		return false, r.RestartCount
	}
	count := r.RestartCount
	if newBoot && r.RestartBootID != boot || r.RestartAt == nil && r.StartedAt != nil && r.FinishedAt != nil && r.FinishedAt.Sub(*r.StartedAt) >= 10*time.Second {
		count = 0
	}
	if limit > 0 && count >= limit {
		return false, count
	}
	return true, count
}
func restartDelay(count uint64) time.Duration {
	if count >= 5 {
		return 30 * time.Second
	}
	return time.Second << count
}
