package image

import (
	"encoding/json"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

// Decode the Docker healthcheck extension independently of the registry
// dependency, whose HealthConfig currently omits StartInterval.
func imageHealthcheck(raw []byte) (*config.HealthConfig, error) {
	var extension struct {
		Config struct {
			Healthcheck *struct {
				Test          []string
				Interval      time.Duration
				Timeout       time.Duration
				StartPeriod   time.Duration
				StartInterval time.Duration
				Retries       int
			}
		}
	}
	if err := json.Unmarshal(raw, &extension); err != nil {
		return nil, err
	}
	h := extension.Config.Healthcheck
	if h == nil {
		return nil, nil
	}
	health := &config.HealthConfig{Test: h.Test}
	if h.Interval != 0 {
		health.Interval = &h.Interval
	}
	if h.Timeout != 0 {
		health.Timeout = &h.Timeout
	}
	if h.StartPeriod != 0 {
		health.StartPeriod = &h.StartPeriod
	}
	if h.StartInterval != 0 {
		health.StartInterval = &h.StartInterval
	}
	if h.Retries != 0 {
		health.Retries = &h.Retries
	}
	if err := health.Validate(); err != nil {
		return nil, err
	}
	if len(health.Test) == 0 {
		return nil, nil
	}
	return health, nil
}
