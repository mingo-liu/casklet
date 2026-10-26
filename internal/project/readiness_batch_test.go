package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func readinessFixture(count int) (Plan, map[string]serviceExecution, []ServiceStatus) {
	plan := Plan{Name: "demo"}
	executions := map[string]serviceExecution{}
	records := []ServiceStatus{}
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("s%d", i)
		id := fmt.Sprintf("%032x", i+1)
		plan.Services = append(plan.Services, Service{Key: key})
		executions[key] = serviceExecution{ID: id, Generation: 2, Healthcheck: true}
		records = append(records, ServiceStatus{ID: id, Project: "demo", Service: key, Generation: 2, State: container.StateRunning, Health: container.HealthHealthy})
	}
	return plan, executions, records
}

func TestReadinessBatchOneCallAndPinnedIdentities(t *testing.T) {
	for _, count := range []int{16, 64} {
		plan, executions, records := readinessFixture(count)
		calls := 0
		runner := runnerWithCall(func(ctx context.Context, args ...string) ([]byte, error) {
			calls++
			expected := []string{"__project-status", "demo"}
			for _, service := range plan.Services {
				expected = append(expected, executions[service.Key].ID)
			}
			if !slices.Equal(args, expected) {
				t.Fatalf("sample did not pin service IDs: %v", args)
			}
			return json.Marshal(records)
		})
		if err := runner.waitServices(context.Background(), plan, executions); err != nil || calls != 1 {
			t.Fatalf("count=%d calls=%d err=%v", count, calls, err)
		}
	}
	for _, failure := range []string{"identity", "generation", "missing", "duplicate", "project label", "service label", "stopping", "failed", "health stopped", "invalid JSON", "call error"} {
		t.Run(failure, func(t *testing.T) {
			plan, executions, records := readinessFixture(2)
			switch failure {
			case "identity":
				records[0].ID = "replaced"
			case "generation":
				records[0].Generation++
			case "missing":
				records = records[1:]
			case "duplicate":
				records = append(records, records[0])
			case "project label":
				records[0].Project = "other"
			case "service label":
				records[0].Service = "other"
			case "stopping":
				records[0].State = container.StateStopping
			case "failed":
				records[0].State = container.StateFailed
			case "health stopped":
				records[0].Health = container.HealthStopped
			}
			runner := runnerWithCall(func(context.Context, ...string) ([]byte, error) {
				if failure == "invalid JSON" {
					return []byte("{"), nil
				}
				if failure == "call error" {
					return nil, errors.New("fixture sample failure")
				}
				return json.Marshal(records)
			})
			if err := runner.waitServices(context.Background(), plan, executions); err == nil {
				t.Fatal("accepted invalid sample")
			}
		})
	}
}

func TestReadinessBatchResamplesDependenciesAndHonorsDeadline(t *testing.T) {
	plan, executions, records := readinessFixture(2)
	samples := 0
	runner := runnerWithCall(func(context.Context, ...string) ([]byte, error) {
		samples++
		if samples == 1 {
			records[1].Health = container.HealthStarting
		} else {
			records[1].Health = container.HealthHealthy
			records[0].State = container.StateExited
		}
		return json.Marshal(records)
	})
	if err := runner.waitServices(context.Background(), plan, executions); err == nil || samples != 2 {
		t.Fatalf("missed stopped dependency: %v samples=%d", err, samples)
	}
	_, _, records = readinessFixture(2)
	records[1].Health = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runner.Call = func(context.Context, ...string) ([]byte, error) { return json.Marshal(records) }
	if err := runner.waitServices(ctx, plan, executions); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	calls := 0
	runner.Call = func(context.Context, ...string) ([]byte, error) { calls++; return nil, nil }
	if err := runner.waitServices(ctx, plan, executions); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("pre-canceled: %v calls=%d", err, calls)
	}
}
