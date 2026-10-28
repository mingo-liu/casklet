package container

import (
	"errors"
	"strings"
	"time"
)

const EventHistoryLimit = 1024

// Event is a sanitized lifecycle observation. It never contains commands,
// environment values, probe output, raw diagnostics or engine filesystem paths.
type Event struct {
	SchemaVersion   int       `json:"schema_version"`
	Sequence        uint64    `json:"sequence"`
	Time            time.Time `json:"time"`
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Type            string    `json:"type"`
	Generation      uint64    `json:"generation"`
	State           string    `json:"state"`
	Health          string    `json:"health,omitempty"`
	ExitCode        *int      `json:"exit_code,omitempty"`
	ExitReason      string    `json:"exit_reason,omitempty"`
	CleanupFailures []string  `json:"cleanup_failures,omitempty"`
	Project         string    `json:"project,omitempty"`
	Service         string    `json:"service,omitempty"`
}

var eventTypes = map[string]bool{"create": true, "starting": true, "start": true, "stopping": true, "exit": true, "restart": true, "stop_requested": true, "restart_scheduled": true, "health": true, "cleanup_failed": true, "remove": true}

type EventFilter struct{ Key, Value string }
type EventQuery struct {
	Follow  bool
	After   *uint64
	Since   time.Time
	Filters []EventFilter
}

func (query EventQuery) Validate() error {
	if len(query.Filters) > 64 {
		return errors.New("events supports at most 64 filters")
	}
	for _, filter := range query.Filters {
		if filter.Value == "" || len(filter.Value) > 128 || strings.ContainsAny(filter.Value, "\x00\r\n") {
			return errors.New("invalid event filter value")
		}
		switch filter.Key {
		case "event":
			if !eventTypes[filter.Value] {
				return errors.New("unknown lifecycle event type")
			}
		case "container", "project", "service":
		default:
			return errors.New("event filters require event, container, project, or service")
		}
	}
	return nil
}
func (query EventQuery) matches(event Event) bool {
	if event.Time.Before(query.Since) {
		return false
	}
	for _, filter := range query.Filters {
		var match bool
		switch filter.Key {
		case "event":
			match = event.Type == filter.Value
		case "container":
			match = event.ID == filter.Value || event.Name == filter.Value
		case "project":
			match = event.Project == filter.Value
		case "service":
			match = event.Service == filter.Value
		}
		if !match {
			return false
		}
	}
	return true
}
