package config

import (
	"errors"
	"strings"
	"time"
)

// HealthConfig keeps explicit zero start periods distinct from inherited values.
// Test uses the Docker image extension: CMD, CMD-SHELL, or NONE.
type HealthConfig struct {
	Shell         []string       `json:"shell,omitempty"`
	Test          []string       `json:"test,omitempty"`
	Interval      *time.Duration `json:"interval,omitempty"`
	Timeout       *time.Duration `json:"timeout,omitempty"`
	StartPeriod   *time.Duration `json:"start_period,omitempty"`
	StartInterval *time.Duration `json:"start_interval,omitempty"`
	Retries       *int           `json:"retries,omitempty"`
}

func (h *HealthConfig) Enabled() bool { return h != nil && len(h.Test) > 0 && h.Test[0] != "NONE" }
func (h *HealthConfig) Validate() error {
	if h == nil {
		return nil
	}
	if len(h.Shell) > 0 {
		if h.Shell[0] == "" {
			return errors.New("healthcheck shell requires a nonempty command")
		}
		size := 0
		for _, arg := range h.Shell {
			if strings.ContainsRune(arg, 0) {
				return errors.New("healthcheck shell cannot contain NUL")
			}
			size += len(arg)
		}
		if size > 32<<10 {
			return errors.New("healthcheck shell exceeds 32 KiB")
		}
	}
	if len(h.Test) > 0 {
		switch h.Test[0] {
		case "NONE":
			if len(h.Test) != 1 {
				return errors.New("disabled healthcheck must contain only NONE")
			}
		case "CMD":
			if len(h.Test) < 2 || h.Test[1] == "" {
				return errors.New("healthcheck CMD requires a nonempty command")
			}
		case "CMD-SHELL":
			if len(h.Test) != 2 || strings.TrimSpace(h.Test[1]) == "" {
				return errors.New("healthcheck CMD-SHELL requires one nonempty command")
			}
		default:
			return errors.New("healthcheck test must use CMD, CMD-SHELL, or NONE")
		}
		size := 0
		for _, arg := range h.Test {
			if strings.ContainsRune(arg, 0) {
				return errors.New("healthcheck arguments cannot contain NUL")
			}
			size += len(arg)
		}
		if size > 32<<10 {
			return errors.New("healthcheck command exceeds 32 KiB")
		}
	}
	for _, duration := range []*time.Duration{h.Interval, h.Timeout, h.StartInterval} {
		if duration != nil && (*duration < time.Millisecond || *duration > 24*time.Hour) {
			return errors.New("healthcheck interval and timeout must be between 1ms and 24h")
		}
	}
	if h.StartPeriod != nil && (*h.StartPeriod < 0 || *h.StartPeriod > 24*time.Hour) {
		return errors.New("healthcheck start period must be between 0s and 24h")
	}
	if h.Retries != nil && (*h.Retries < 1 || *h.Retries > 1000) {
		return errors.New("healthcheck retries must be between 1 and 1000")
	}
	return nil
}

// MergeHealth copies image defaults and replaces only explicitly supplied fields.
func MergeHealth(defaults, overrides *HealthConfig) *HealthConfig {
	if defaults == nil && overrides == nil {
		return nil
	}
	result := HealthConfig{}
	if defaults != nil {
		result = *defaults
		result.Test = append([]string(nil), defaults.Test...)
		result.Shell = append([]string(nil), defaults.Shell...)
	}
	if overrides != nil {
		if overrides.Shell != nil {
			result.Shell = append([]string(nil), overrides.Shell...)
		}
		if overrides.Test != nil {
			result.Test = append([]string(nil), overrides.Test...)
		}
		if overrides.Interval != nil {
			result.Interval = overrides.Interval
		}
		if overrides.Timeout != nil {
			result.Timeout = overrides.Timeout
		}
		if overrides.StartPeriod != nil {
			result.StartPeriod = overrides.StartPeriod
		}
		if overrides.StartInterval != nil {
			result.StartInterval = overrides.StartInterval
		}
		if overrides.Retries != nil {
			result.Retries = overrides.Retries
		}
	}
	return &result
}

func healthDuration(value *time.Duration, fallback time.Duration) time.Duration {
	if value != nil {
		return *value
	}
	return fallback
}
func (h *HealthConfig) IntervalValue() time.Duration {
	return healthDuration(h.Interval, 30*time.Second)
}
func (h *HealthConfig) TimeoutValue() time.Duration     { return healthDuration(h.Timeout, 30*time.Second) }
func (h *HealthConfig) StartPeriodValue() time.Duration { return healthDuration(h.StartPeriod, 0) }
func (h *HealthConfig) StartIntervalValue() time.Duration {
	return healthDuration(h.StartInterval, 5*time.Second)
}
func (h *HealthConfig) RetriesValue() int {
	if h.Retries != nil {
		return *h.Retries
	}
	return 3
}
func (h *HealthConfig) Command() []string {
	if h.Test[0] == "CMD-SHELL" {
		shell := h.Shell
		if len(shell) == 0 {
			shell = []string{"/bin/sh", "-c"}
		}
		return append(append([]string(nil), shell...), h.Test[1])
	}
	return append([]string(nil), h.Test[1:]...)
}
