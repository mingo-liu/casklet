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
}

func (*cleanupGroup) Path() string  { return "/test/cgroup" }
func (g *cleanupGroup) Kill() error { return g.killErr }
func (g *cleanupGroup) WaitEmpty(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("missing cleanup deadline")
	}
	return g.emptyErr
}
func (*cleanupGroup) OOMKilled() (bool, error) { return false, nil }
func (g *cleanupGroup) Close() error           { g.closed = true; return g.closeErr }

func TestWorkloadCleanupRecordsFailuresAndPreservesRecoveryState(t *testing.T) {
	failure := errors.New("cleanup failed")
	for _, tt := range []struct {
		name         string
		group        cleanupGroup
		stages       []string
		keep, closed bool
	}{
		{"success", cleanupGroup{}, nil, false, true},
		{"kill", cleanupGroup{killErr: failure}, []string{"cgroup.kill"}, false, true},
		{"not-empty", cleanupGroup{emptyErr: failure}, []string{"cgroup.empty"}, true, false},
		{"remove", cleanupGroup{closeErr: failure}, []string{"cgroup.remove"}, true, true},
		{"kill-and-empty", cleanupGroup{killErr: failure, emptyErr: failure}, []string{"cgroup.kill", "cgroup.empty"}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			run, err := createRunAt(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if err := run.record(tt.group.Path()); err != nil {
				t.Fatal(err)
			}
			err = cleanupWorkload(&tt.group, run, io.Discard)
			if !reflect.DeepEqual(CleanupStages(err), tt.stages) || run.keep != tt.keep || tt.group.closed != tt.closed {
				t.Fatalf("cleanup=%v keep=%v closed=%v", err, run.keep, tt.group.closed)
			}
			if len(tt.stages) > 0 && !errors.Is(err, failure) {
				t.Fatal("failure cause lost")
			}
			if err := run.remove(); err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(filepath.Join(run.path, "state.json"))
			if tt.keep && statErr != nil {
				t.Fatal("recovery receipt was removed", statErr)
			}
			if !tt.keep && !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("successful cleanup left a receipt", statErr)
			}
		})
	}
}
