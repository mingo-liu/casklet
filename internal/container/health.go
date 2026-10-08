package container

import (
	"errors"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

const (
	HealthStarting  = "starting"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthStopped   = "stopped"
)

// Health retains bounded probe diagnostics without output or environment values.
type Health struct {
	Status        string         `json:"status"`
	FailingStreak int            `json:"failing_streak"`
	Checks        []HealthResult `json:"checks"`
}

type HealthResult struct {
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	ExitCode        int       `json:"exit_code"`
	TimedOut        bool      `json:"timed_out"`
	ExecutionFailed bool      `json:"execution_failed"`
}

func (h *Health) validate() error {
	if h == nil {
		return nil
	}
	switch h.Status {
	case HealthStarting, HealthHealthy, HealthUnhealthy, HealthStopped:
	default:
		return errors.New("invalid recorded health status")
	}
	if h.FailingStreak < 0 || h.FailingStreak > 1000 || len(h.Checks) > 5 {
		return errors.New("invalid recorded health results")
	}
	for _, check := range h.Checks {
		if check.StartedAt.IsZero() || check.FinishedAt.Before(check.StartedAt) || check.ExitCode < 0 || check.ExitCode > 255 {
			return errors.New("invalid recorded health result")
		}
	}
	return nil
}

func (h Health) next(cfg *config.HealthConfig, started time.Time, result HealthResult) Health {
	h.Checks = append(append([]HealthResult(nil), h.Checks...), result)
	if len(h.Checks) > 5 {
		h.Checks = h.Checks[len(h.Checks)-5:]
	}
	if result.ExitCode == 0 && !result.ExecutionFailed && !result.TimedOut {
		h.Status = HealthHealthy
		h.FailingStreak = 0
		return h
	}
	if h.Status == HealthStarting && result.StartedAt.Sub(started) < cfg.StartPeriodValue() {
		return h
	}
	if h.FailingStreak < cfg.RetriesValue() {
		h.FailingStreak++
	}
	if h.FailingStreak >= cfg.RetriesValue() {
		h.Status = HealthUnhealthy
	}
	return h
}

func (h Health) delay(cfg *config.HealthConfig, started, now time.Time) time.Duration {
	if h.Status == HealthStarting && now.Sub(started) < cfg.StartPeriodValue() {
		return cfg.StartIntervalValue()
	}
	return cfg.IntervalValue()
}

func (record Record) effectiveHealth() *Health {
	if record.Health == nil {
		return nil
	}
	health := *record.Health
	health.Checks = append([]HealthResult{}, health.Checks...)
	if record.Terminal() || record.State == StateStopping {
		health.Status = HealthStopped
	}
	return &health
}
