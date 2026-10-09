package container

import (
	"fmt"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
)

// Filter is a validated listing predicate. Repeated predicates are intersected.
type Filter struct {
	kind  string
	key   string
	value string
	exact bool
}

func ParseFilter(expression string) (Filter, error) {
	kind, value, ok := strings.Cut(expression, "=")
	if !ok {
		return Filter{}, fmt.Errorf("filter requires label=KEY[=VALUE], status=STATE, or health=STATUS")
	}
	f := Filter{kind: kind, value: value}
	switch kind {
	case "label":
		f.key, f.value, f.exact = strings.Cut(value, "=")
		if f.exact {
			if _, _, err := config.ParseLabel(value); err != nil {
				return Filter{}, err
			}
		} else if err := config.ValidateLabelKey(f.key); err != nil {
			return Filter{}, err
		}
	case "status":
		switch value {
		case StateCreated, StateStarting, StateRunning, StateStopping, StateExited, StateFailed:
		default:
			return Filter{}, fmt.Errorf("status filter must be created, starting, running, stopping, exited, or failed")
		}
	case "health":
		switch value {
		case "none", HealthStarting, HealthHealthy, HealthUnhealthy, HealthStopped:
		default:
			return Filter{}, fmt.Errorf("health filter must be none, starting, healthy, unhealthy, or stopped")
		}
	default:
		return Filter{}, fmt.Errorf("unknown container filter %q; use label, status, or health", kind)
	}
	return f, nil
}

func (f Filter) matches(record Record) bool {
	switch f.kind {
	case "label":
		value, present := record.Labels[f.key]
		return present && (!f.exact || value == f.value)
	case "status":
		return record.State == f.value
	case "health":
		health := record.effectiveHealth()
		if health == nil {
			return f.value == "none"
		}
		return health.Status == f.value
	}
	return false
}

// FilterRecords preserves listing order and requires every predicate to match.
func FilterRecords(records []Record, filters []Filter) []Record {
	selected := make([]Record, 0, len(records))
	for _, record := range records {
		matches := true
		for _, filter := range filters {
			if !filter.matches(record) {
				matches = false
				break
			}
		}
		if matches {
			selected = append(selected, record)
		}
	}
	return selected
}
