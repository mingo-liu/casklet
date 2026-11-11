//go:build linux

package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type cleanupGroup struct {
	killErr, emptyErr, closeErr error
	closed                      bool
	oom                         bool
	oomErr                      error
}

func TestWorkloadCleanupRecordsFailuresAndPreservesRecoveryState(t *testing.T) {
	failure := errors.New("cleanup failed")
	tests := []struct {
		name         string
		group        cleanupGroup
		stages       []string
		keep, closed bool
	}{
		{name: "success", group: cleanupGroup{}, stages: nil, keep: false, closed: true},
		{name: "kill", group: cleanupGroup{killErr: failure}, stages: []string{"cgroup.kill"}, keep: false, closed: true},
		{name: "not-empty", group: cleanupGroup{emptyErr: failure}, stages: []string{"cgroup.empty"}, keep: true, closed: false},
		{name: "remove", group: cleanupGroup{closeErr: failure}, stages: []string{"cgroup.remove"}, keep: true, closed: true},
		{name: "kill-and-empty", group: cleanupGroup{killErr: failure, emptyErr: failure}, stages: []string{"cgroup.kill", "cgroup.empty"}, keep: true, closed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			run, err := createRunAt(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if err := run.record(tt.group.Path()); err != nil {
				t.Fatal(err)
			}
			_, err = cleanupWorkload(&tt.group, run, io.Discard)
			if !reflect.DeepEqual(CleanupStages(err), tt.stages) || run.keep != tt.keep || tt.group.closed != tt.closed {
				t.Fatalf("cleanup=%v keep=%v closed=%v", err, run.keep, tt.group.closed)
			}
			if errors.Is(err, failure) != (len(tt.stages) > 0) {
				t.Fatal("failure cause lost")
			}
			if err := run.remove(); err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(filepath.Join(run.path, "state.json"))
			if (statErr == nil) != tt.keep {
				t.Fatalf("recovery receipt: error=%v, want retained=%v", statErr, tt.keep)
			}
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal(statErr)
			}
		})
	}
}

func TestWorkloadCleanupRetainsObservedOOMBeforeCgroupRemoval(t *testing.T) {
	tests := []struct {
		name  string
		group cleanupGroup
		want  bool
	}{
		{"oom", cleanupGroup{oom: true}, true},
		{"oom-and-cleanup-failure", cleanupGroup{oom: true, closeErr: errors.New("remove failed")}, true},
		{"unknown", cleanupGroup{oom: true, oomErr: errors.New("read failed")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run, err := createRunAt(context.Background(), t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = run.lock.Close() }()
			oom, _ := cleanupWorkload(&tt.group, run, io.Discard)
			if oom != tt.want || !tt.group.closed {
				t.Fatalf("oom=%v closed=%v", oom, tt.group.closed)
			}
		})
	}
}

func (*cleanupGroup) Path() string { return "/test/cgroup" }

func (g *cleanupGroup) Kill() error { return g.killErr }

func (g *cleanupGroup) WaitEmpty(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing cleanup deadline")
	}
	return g.emptyErr
}

func (g *cleanupGroup) OOMKilled() (bool, error) { return g.oom, g.oomErr }

func (g *cleanupGroup) Close() error { g.closed = true; return g.closeErr }
