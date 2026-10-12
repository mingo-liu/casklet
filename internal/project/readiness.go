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
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ready := true
		for _, service := range plan.Services {
			execution := executions[service.Key]
			latest, err := runner.inspectService(ctx, service.Key, execution.ID)
			if err != nil {
				return err
			}
			if latest.Generation != execution.Generation {
				return fmt.Errorf("service %q execution changed before project readiness", service.Key)
			}
			if latest.State == container.StateExited || latest.State == container.StateFailed || latest.State == container.StateStopping || latest.Health != nil && latest.Health.Status == container.HealthStopped {
				return fmt.Errorf("service %q stopped before project readiness (state: %s)", service.Key, latest.State)
			}
			ready = execution.ready(latest.State, latest.Health) && ready
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
