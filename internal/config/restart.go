package config

import (
	"errors"
	"strconv"
	"strings"
)

// ParseRestartPolicy returns the mode and optional consecutive automatic retry cap.
func ParseRestartPolicy(value string) (string, uint64, error) {
	if value == "" || value == "no" {
		return "no", 0, nil
	}
	if value == "always" || value == "unless-stopped" || value == "on-failure" {
		return value, 0, nil
	}
	if suffix, ok := strings.CutPrefix(value, "on-failure:"); ok && suffix != "" && strings.Trim(suffix, "0123456789") == "" {
		n, err := strconv.ParseUint(suffix, 10, 32)
		if err == nil && n > 0 && n <= 1000 {
			return "on-failure", n, nil
		}
	}
	return "", 0, errors.New("restart policy must be no, always, unless-stopped, or on-failure[:1-1000]")
}
func (c Config) RestartMode() string { mode, _, _ := ParseRestartPolicy(c.RestartPolicy); return mode }
