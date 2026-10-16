package project

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

// Simulate image-inherited healthchecks absent from the explicit project plan.
func healthInspection(t *testing.T, data []byte, status string) []byte {
	t.Helper()
	var value container.Inspection
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	value.Config.Healthcheck = &container.InspectionHealthConfig{Test: []string{"CMD-SHELL", "true"}}
	value.Health = &container.Health{Status: status}
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestProjectWaitIncludesLeafHealthAndKeepsDefaultBehavior(t *testing.T) {
	for _, wait := range []bool{false, true} {
		fake := newFakeProject()
		runner := fakeRunner(fake)
		plan := fakePlan()
		plan.Services[1].DependsOn = nil
		inspections, samples := 0, 0
		runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
			if args[0] == "__project-status" {
				samples++
				status := container.HealthUnhealthy
				if samples >= 2 {
					status = container.HealthHealthy
				}
				fake.records[1].Health = &container.Health{Status: status}
			}
			data, err := fake.call(ctx, args...)
			if err == nil && args[0] == "inspect" && len(fake.records) > 1 && args[1] == fake.records[1].ID {
				inspections++
				data = healthInspection(t, data, container.HealthUnhealthy)
			}
			return data, err
		}
		records, err := runner.Up(context.Background(), plan, UpOptions{Wait: wait, Timeout: time.Second})
		if err != nil || len(records) != 2 {
			t.Fatalf("wait=%v records=%v err=%v", wait, records, err)
		}
		if wait && (inspections != 1 || samples < 2) || !wait && (inspections != 0 || samples != 0) {
			t.Fatalf("wait=%v inspections=%d", wait, inspections)
		}
		if wait && records[1].Health != nil {
			t.Fatal("service without a healthcheck should only need running state")
		}
	}
}

func TestProjectWaitDeadlinePreservesReusedServicesAndRollsBackNewOnes(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	plan.Services[1].DependsOn = nil
	initial := plan
	initial.Services = initial.Services[:1]
	if _, err := runner.Up(context.Background(), initial, UpOptions{Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	original := fake.records[0]
	resources := len(fake.resources)
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		data, err := fake.call(ctx, args...)
		if err == nil && args[0] == "inspect" && args[1] != original.ID {
			data = healthInspection(t, data, container.HealthStarting)
		}
		return data, err
	}
	_, err := runner.Up(context.Background(), plan, UpOptions{Wait: true, Timeout: 30 * time.Millisecond})
	if !errors.Is(err, ErrWaitTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	if !reflect.DeepEqual(fake.records, []container.Record{original}) || len(fake.resources) != resources {
		t.Fatalf("reused state changed: %v %v", fake.records, fake.resources)
	}
}

func TestProjectWaitDeadlineIncludesStartupAndUsesOneBudget(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	plan.Services[1].DependsOn = nil
	var deadline time.Time
	calls := 0
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			current, ok := ctx.Deadline()
			if !ok || !deadline.IsZero() && !current.Equal(deadline) {
				t.Fatal("deployment reset its deadline")
			}
			deadline = current
			calls++
			if calls == 2 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
		}
		return fake.call(ctx, args...)
	}
	_, err := runner.Up(context.Background(), plan, UpOptions{Wait: true, Timeout: 30 * time.Millisecond})
	if !errors.Is(err, ErrWaitTimeout) || calls != 2 || len(fake.records) != 0 || len(fake.resources) != 0 {
		t.Fatalf("deadline=%v calls=%d state=%v %v", err, calls, fake.records, fake.resources)
	}
}

func TestProjectWaitRejectsChangedExecutionAndStoppedService(t *testing.T) {
	for _, change := range []string{"generation", "identity", "stopped", "missing", "final state"} {
		t.Run(change, func(t *testing.T) {
			fake := newFakeProject()
			runner := fakeRunner(fake)
			plan := fakePlan()
			plan.Services = plan.Services[:1]
			runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
				data, err := fake.call(ctx, args...)
				if err == nil && args[0] == "__project-status" {
					var values []ServiceStatus
					if err := json.Unmarshal(data, &values); err != nil {
						t.Fatal(err)
					}
					switch change {
					case "generation":
						values[0].Generation++
					case "identity":
						values[0].ID = "replaced"
					case "stopped":
						values[0].State = container.StateExited
					case "missing":
						values = nil
					case "final state":
						fake.records[0].State = container.StateExited
					}
					data, _ = json.Marshal(values)
				}
				return data, err
			}
			_, err := runner.Up(context.Background(), plan, UpOptions{Wait: true, Timeout: time.Second})
			if err == nil || errors.Is(err, ErrWaitTimeout) || len(fake.records) != 0 {
				t.Fatalf("change=%s err=%v records=%v", change, err, fake.records)
			}
		})
	}
}

func TestProjectWaitCancellationAndCleanupErrorsRemainVisible(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		fake := newFakeProject()
		runner := fakeRunner(fake)
		plan := fakePlan()
		plan.Services = plan.Services[:1]
		ctx, cancel := context.WithCancel(context.Background())
		if callerDeadline {
			ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
		}
		defer cancel()
		runner.Call = func(waiting context.Context, args ...string) ([]byte, error) {
			if args[0] == "inspect" {
				if callerDeadline {
					<-waiting.Done()
				} else {
					cancel()
				}
				return nil, waiting.Err()
			}
			return fake.call(waiting, args...)
		}
		_, err := runner.Up(ctx, plan, UpOptions{Wait: true, Timeout: time.Second})
		if err == nil || errors.Is(err, ErrWaitTimeout) || len(fake.records) != 0 || len(fake.resources) != 0 {
			t.Fatalf("caller deadline=%v err=%v state=%v", callerDeadline, err, fake.records)
		}
	}
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	plan.Services = plan.Services[:1]
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if args[0] == "rm" {
			return nil, errors.New("cleanup fixture failure")
		}
		return fake.call(ctx, args...)
	}
	_, err := runner.Up(context.Background(), plan, UpOptions{Wait: true, Timeout: 20 * time.Millisecond})
	if !errors.Is(err, ErrWaitTimeout) || !strings.Contains(err.Error(), "cleanup fixture failure") || len(fake.records) != 1 {
		t.Fatalf("lost cleanup diagnostic: %v", err)
	}
}

func TestProjectServiceFailureIsNotReclassifiedBySlowRollback(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	plan.Services = plan.Services[:1]
	var deadline time.Time
	failure := errors.New("service fixture failure")
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			deadline, _ = ctx.Deadline()
			return nil, failure
		}
		if args[0] == "rm" {
			timer := time.NewTimer(max(0, time.Until(deadline)+10*time.Millisecond))
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return fake.call(ctx, args...)
	}
	_, err := runner.Up(context.Background(), plan, UpOptions{Wait: true, Timeout: 30 * time.Millisecond})
	if !errors.Is(err, failure) || errors.Is(err, ErrWaitTimeout) || len(fake.records) != 0 {
		t.Fatalf("misclassified failure=%v", err)
	}
}

func TestProjectWaitHonorsDisabledHealthAndInheritedDeadline(t *testing.T) {
	fake := newFakeProject()
	runner := fakeRunner(fake)
	plan := fakePlan()
	plan.Services = plan.Services[:1]
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		data, err := fake.call(ctx, args...)
		if err == nil && args[0] == "inspect" {
			var value container.Inspection
			json.Unmarshal(data, &value)
			value.Config.Healthcheck = &container.InspectionHealthConfig{Test: []string{"NONE"}}
			data, _ = json.Marshal(value)
		}
		return data, err
	}
	if _, err := runner.Up(context.Background(), plan, UpOptions{Wait: true, Timeout: time.Second}); err != nil {
		t.Fatal("disabled healthcheck", err)
	}
	runner.Call = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return fake.call(ctx, args...)
	}
	_, err := runner.Up(context.Background(), plan, UpOptions{Wait: true, Timeout: time.Second, Deadline: time.Now().Add(20 * time.Millisecond)})
	if !errors.Is(err, ErrWaitTimeout) || len(fake.records) != 1 {
		t.Fatalf("inherited budget=%v records=%v", err, fake.records)
	}
}
