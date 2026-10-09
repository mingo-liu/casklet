package container

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitHealthExecutionTransitions(t *testing.T) {
	initial := Record{ID: "immutable", Generation: 4, State: StateStarting}
	states := []Record{
		initial,
		{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthStarting}},
		{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthUnhealthy}},
		{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthHealthy}},
	}
	reads := 0
	err := waitHealthExecution(context.Background(), initial, time.Second, func(ctx context.Context, id string) (Record, error) {
		if id != initial.ID || ctx.Err() != nil {
			t.Fatalf("reader received %q, %v", id, ctx.Err())
		}
		r := states[reads]
		reads++
		return r, nil
	})
	if err != nil || reads != len(states) {
		t.Fatalf("readiness: reads=%d error=%v", reads, err)
	}
}

func TestWaitHealthExecutionImmediateAndFailures(t *testing.T) {
	initial := Record{ID: "immutable", Generation: 4, State: StateRunning}
	missing := errors.New("container not found")
	for _, test := range []struct {
		name   string
		latest Record
		err    error
		want   string
	}{
		{"healthy", Record{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthHealthy}}, nil, ""},
		{"stopping despite healthy", Record{ID: initial.ID, Generation: 4, State: StateStopping, Health: &Health{Status: HealthHealthy}}, nil, "stopped"},
		{"exited despite healthy", Record{ID: initial.ID, Generation: 4, State: StateExited, Health: &Health{Status: HealthHealthy}}, nil, "stopped"},
		{"failed", Record{ID: initial.ID, Generation: 4, State: StateFailed}, nil, "stopped"},
		{"health stopped", Record{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthStopped}}, nil, "stopped"},
		{"new execution", Record{ID: initial.ID, Generation: 5, State: StateRunning, Health: &Health{Status: HealthHealthy}}, nil, "execution changed"},
		{"reused name", Record{ID: "replacement", Generation: 4, State: StateRunning, Health: &Health{Status: HealthHealthy}}, nil, "execution changed"},
		{"removed", Record{}, missing, "container not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			reads := 0
			err := waitHealthExecution(context.Background(), initial, time.Second, func(context.Context, string) (Record, error) { reads++; return test.latest, test.err })
			if reads != 1 || test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("reads=%d error=%v", reads, err)
			}
		})
	}
}

func TestWaitHealthExecutionDeadlineAndCancellation(t *testing.T) {
	initial := Record{ID: "immutable", State: StateRunning, Health: &Health{Status: HealthUnhealthy}}
	for _, test := range []struct {
		name          string
		timeout       time.Duration
		parentTimeout time.Duration
		want          error
	}{
		{"own deadline", 20 * time.Millisecond, time.Second, ErrWaitTimeout},
		{"parent deadline", time.Second, 20 * time.Millisecond, context.DeadlineExceeded},
		{"unbounded observes parent", 0, 20 * time.Millisecond, context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), test.parentTimeout)
			defer cancel()
			reads := 0
			started := time.Now()
			err := waitHealthExecution(ctx, initial, test.timeout, func(context.Context, string) (Record, error) { reads++; return initial, nil })
			if !errors.Is(err, test.want) || reads != 1 || time.Since(started) > time.Second {
				t.Fatalf("error=%v reads=%d duration=%s", err, reads, time.Since(started))
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitHealthExecution(ctx, initial, 0, func(context.Context, string) (Record, error) { t.Fatal("read after cancellation"); return initial, nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestWaitHealthExecutionBoundsBlockedRead(t *testing.T) {
	initial := Record{ID: "immutable"}
	err := waitHealthExecution(context.Background(), initial, 20*time.Millisecond, func(ctx context.Context, _ string) (Record, error) {
		<-ctx.Done()
		return Record{}, ctx.Err()
	})
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatal(err)
	}
}

func TestHealthWaitDeadlineIncludesInitialResolution(t *testing.T) {
	err := withHealthWaitDeadline(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done() // Models a store snapshot waiting on its metadata lock.
		return ctx.Err()
	})
	if !errors.Is(err, ErrWaitTimeout) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = withHealthWaitDeadline(ctx, time.Second, func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := withHealthWaitDeadline(context.Background(), -time.Second, func(context.Context) error { t.Fatal("operation with invalid timeout"); return nil }); err == nil {
		t.Fatal("accepted negative deadline")
	}
}
