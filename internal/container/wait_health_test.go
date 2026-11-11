package container

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitHealthExecutionTransitions(t *testing.T) {
	var (
		initial = Record{ID: "immutable", Generation: 4, State: StateStarting}
		states  = []Record{
			initial,
			{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthStarting}},
			{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthUnhealthy}},
			{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthHealthy}},
		}
		reads = 0
		err   = waitHealthExecution(context.Background(), initial, time.Second, func(ctx context.Context, id string) (Record, error) {
			if id != initial.ID || ctx.Err() != nil {
				t.Fatalf("reader received %q, %v", id, ctx.Err())
			}
			r := states[reads]
			reads++
			return r, nil
		})
	)
	if err != nil || reads != len(states) {
		t.Fatalf("readiness: reads=%d error=%v", reads, err)
	}
}

func TestWaitHealthExecutionImmediateAndFailures(t *testing.T) {
	var (
		initial = Record{ID: "immutable", Generation: 4, State: StateRunning}
		missing = errors.New("container not found")
	)
	tests := []struct {
		name   string
		latest Record
		err    error
		want   string
	}{
		{
			name:   "healthy",
			latest: Record{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthHealthy}},
			err:    nil,
			want:   "",
		},
		{
			name:   "stopping despite healthy",
			latest: Record{ID: initial.ID, Generation: 4, State: StateStopping, Health: &Health{Status: HealthHealthy}},
			err:    nil,
			want:   "stopped",
		},
		{
			name:   "exited despite healthy",
			latest: Record{ID: initial.ID, Generation: 4, State: StateExited, Health: &Health{Status: HealthHealthy}},
			err:    nil,
			want:   "stopped",
		},
		{name: "failed", latest: Record{ID: initial.ID, Generation: 4, State: StateFailed}, err: nil, want: "stopped"},
		{
			name:   "health stopped",
			latest: Record{ID: initial.ID, Generation: 4, State: StateRunning, Health: &Health{Status: HealthStopped}},
			err:    nil,
			want:   "stopped",
		},
		{
			name:   "new execution",
			latest: Record{ID: initial.ID, Generation: 5, State: StateRunning, Health: &Health{Status: HealthHealthy}},
			err:    nil,
			want:   "execution changed",
		},
		{
			name:   "reused name",
			latest: Record{ID: "replacement", Generation: 4, State: StateRunning, Health: &Health{Status: HealthHealthy}},
			err:    nil,
			want:   "execution changed",
		},
		{name: "removed", latest: Record{}, err: missing, want: "container not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				reads = 0
				err   = waitHealthExecution(context.Background(), initial, time.Second, func(context.Context, string) (Record, error) { reads++; return tt.latest, tt.err })
			)
			if reads != 1 || tt.want == "" && err != nil || tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("reads=%d error=%v", reads, err)
			}
		})
	}
}

func TestWaitHealthExecutionDeadlineAndCancellation(t *testing.T) {
	initial := Record{ID: "immutable", State: StateRunning, Health: &Health{Status: HealthUnhealthy}}
	tests := []struct {
		name          string
		timeout       time.Duration
		parentTimeout time.Duration
		want          error
	}{
		{name: "own deadline", timeout: 20 * time.Millisecond, parentTimeout: time.Second, want: ErrWaitTimeout},
		{
			name:          "parent deadline",
			timeout:       time.Second,
			parentTimeout: 20 * time.Millisecond,
			want:          context.DeadlineExceeded,
		},
		{
			name:          "unbounded observes parent",
			timeout:       0,
			parentTimeout: 20 * time.Millisecond,
			want:          context.DeadlineExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tt.parentTimeout)
			defer cancel()
			var (
				reads   = 0
				started = time.Now()
				err     = waitHealthExecution(ctx, initial, tt.timeout, func(context.Context, string) (Record, error) { reads++; return initial, nil })
			)
			if !errors.Is(err, tt.want) || reads != 1 || time.Since(started) > time.Second {
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
	var (
		initial = Record{ID: "immutable"}
		err     = waitHealthExecution(context.Background(), initial, 20*time.Millisecond, func(ctx context.Context, _ string) (Record, error) {
			<-ctx.Done()
			return Record{}, ctx.Err()
		})
	)
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
