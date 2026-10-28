package project

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func taskFixture(code int, reason string, running bool) (*fakeProject, *Runner, Plan) {
	fake := newFakeProject()
	plan := fakePlan()
	plan.Services[0].Task = true
	plan.Services[1].DependsOn = map[string]string{"db": "completed"}
	call := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "__project-status" {
			values := []ServiceStatus{}
			for _, r := range fake.records {
				if slices.Contains(args[2:], r.ID) {
					values = append(values, ServiceStatus{ID: r.ID, Project: "demo", Service: r.Labels[LabelService], Generation: r.Generation, State: r.State, ExitCode: r.ExitCode, ExitReason: r.ExitReason})
				}
			}
			return json.Marshal(values)
		}
		data, err := fake.call(ctx, args...)
		if err == nil && args[0] == "run" && fake.records[len(fake.records)-1].Name == "cp-demo-db" && !running {
			r := &fake.records[len(fake.records)-1]
			r.State = container.StateExited
			r.ExitCode = &code
			r.ExitReason = reason
		}
		return data, err
	}
	runner := fakeRunner(fake)
	runner.Call = call
	runner.Backend = CommandBackend{Call: call}
	return fake, runner, plan
}
func TestProjectTaskCompletesBeforeDependentsAndIsReused(t *testing.T) {
	fake, runner, plan := taskFixture(0, "completed", false)
	records, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second, Wait: true})
	if err != nil || len(records) != 2 || records[1].State != container.StateExited {
		t.Fatalf("up %v %v", records, err)
	}
	count := len(fake.mutations)
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if len(fake.mutations) != count {
		t.Fatalf("reran task %v", fake.mutations)
	}
}
func TestProjectTaskFailureBlocksDependentsAndRollsBackOnlyNew(t *testing.T) {
	fake, runner, plan := taskFixture(7, "completed", false)
	_, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second})
	var failure *TaskFailure
	if !errors.As(err, &failure) || failure.Code != 7 {
		t.Fatalf("failure %v", err)
	}
	for _, call := range fake.mutations {
		if strings.Contains(call, "run cp-demo-api") {
			t.Fatal("started dependent")
		}
	}
	if len(fake.records) != 0 {
		t.Fatal("did not roll back failed new task")
	}
	fake, runner, plan = taskFixture(0, "stopped", false)
	if _, err := runner.Up(context.Background(), plan, UpOptions{Timeout: time.Second}); err == nil {
		t.Fatal("accepted a stopped task as success")
	}
}
func TestProjectTaskDeadlineAppliesWithoutWait(t *testing.T) {
	_, runner, plan := taskFixture(0, "completed", true)
	_, err := runner.Up(context.Background(), plan, UpOptions{Timeout: 30 * time.Millisecond})
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatalf("deadline %v", err)
	}
}
func TestProjectCompletedDependencyRequiresTask(t *testing.T) {
	for _, document := range []string{
		`{"name":"demo","services":{"job":{"run":{}},"app":{"run":{},"depends-on":{"job":"completed"}}}}`,
		`{"name":"demo","services":{"job":{"task":true,"run":{}},"app":{"run":{},"depends-on":{"job":"started"}}}}`,
		`{"name":"demo","services":{"job":{"task":true,"run":{}},"app":{"run":{},"depends-on":{"job":"healthy"}}}}`,
	} {
		if _, err := Decode([]byte(document)); err == nil {
			t.Fatal("accepted invalid task dependency")
		}
	}
}
