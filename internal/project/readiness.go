package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

// ErrWaitTimeout distinguishes the deployment deadline from a service failure.
var ErrWaitTimeout = errors.New("timed out waiting for project readiness")

// ServiceStatus is the bounded guest response used by project readiness passes.
// Configuration, command arguments and probe output are intentionally excluded.
type ServiceStatus struct {
	ID         string `json:"id"`
	Project    string `json:"project"`
	Service    string `json:"service"`
	Generation uint64 `json:"generation"`
	State      string `json:"state"`
	Health     string `json:"health,omitempty"`
}

type serviceExecution struct {
	ID          string
	Generation  uint64
	Healthcheck bool
}

func (execution serviceExecution) ready(state string, health *container.Health) bool {
	return state == container.StateRunning && (!execution.Healthcheck || health != nil && health.Status == container.HealthHealthy)
}

func (runner *Runner) inspectService(ctx context.Context, key, id string) (container.Inspection, error) {
	data, err := runner.Call(ctx, "inspect", id)
	if err != nil {
		return container.Inspection{}, fmt.Errorf("inspect service %q: %w", key, err)
	}
	var value container.Inspection
	if json.Unmarshal(data, &value) != nil || value.ID != id {
		return value, fmt.Errorf("invalid inspection identity for service %q", key)
	}
	return value, nil
}

// Sample all services again after each wait so a dependency that stopped while
// a leaf was becoming healthy cannot leave the whole project falsely ready.
func (runner *Runner) waitServices(ctx context.Context, plan Plan, executions map[string]serviceExecution) error {
	args := []string{"__project-status", plan.Name}
	for _, service := range plan.Services {
		args = append(args, executions[service.Key].ID)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// One guest invocation samples minimal lifecycle/health state for the
		// project. Keep per-service ID and execution fencing from startup.
		data, err := runner.Call(ctx, args...)
		if err != nil {
			return fmt.Errorf("sample project readiness: %w", err)
		}
		var records []ServiceStatus
		if json.Unmarshal(data, &records) != nil {
			return errors.New("invalid project readiness response")
		}
		byID := make(map[string]ServiceStatus, len(records))
		for _, record := range records {
			if _, exists := byID[record.ID]; exists {
				return errors.New("duplicate project readiness identity")
			}
			byID[record.ID] = record
		}
		ready := true
		for _, service := range plan.Services {
			execution := executions[service.Key]
			latest, found := byID[execution.ID]
			if !found {
				return fmt.Errorf("service %q missing before project readiness: %w", service.Key, container.ErrNotFound)
			}
			if latest.Project != plan.Name || latest.Service != service.Key {
				return fmt.Errorf("invalid readiness identity for service %q", service.Key)
			}
			if latest.Generation != execution.Generation {
				return fmt.Errorf("service %q execution changed before project readiness", service.Key)
			}
			if latest.State == container.StateExited || latest.State == container.StateFailed || latest.State == container.StateStopping || latest.Health == container.HealthStopped {
				return fmt.Errorf("service %q stopped before project readiness (state: %s)", service.Key, latest.State)
			}
			ready = execution.ready(latest.State, &container.Health{Status: latest.Health}) && ready
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
