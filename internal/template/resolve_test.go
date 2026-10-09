package template

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/image"
)

type fakeImageSource struct {
	record     image.Record
	tree       string
	lease      *os.File
	missing    bool
	resolveErr error
	pulls      int
}

func (s *fakeImageSource) Resolve(context.Context, string) (image.Record, error) {
	if s.resolveErr != nil {
		return image.Record{}, s.resolveErr
	}
	if s.missing {
		return image.Record{}, image.ErrNotFound
	}
	return s.record, nil
}
func (s *fakeImageSource) Pull(context.Context, string) (image.Record, error) {
	s.pulls++
	return s.record, nil
}
func (s *fakeImageSource) Acquire(context.Context, string) (image.Record, string, *os.File, error) {
	return s.record, s.tree, s.lease, nil
}

func TestResolveExecutionPinsImageAndKeepsLease(t *testing.T) {
	for _, missing := range []bool{false, true} {
		s := &fakeImageSource{record: image.Record{ID: "sha256:" + strings.Repeat("a", 64), Config: &image.LaunchConfig{Cmd: []string{"server"}}}, tree: t.TempDir(), missing: missing}
		var err error
		s.lease, err = os.CreateTemp(t.TempDir(), "lease")
		if err != nil {
			t.Fatal(err)
		}
		cfg, lease, err := resolveExecution(context.Background(), config.Config{Image: "redis:8"}, nil, s)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Image != s.record.ID || !cfg.OCI || len(cfg.Command) != 1 || cfg.Command[0] != "server" {
			t.Fatalf("resolved config: %+v", cfg)
		}
		if (s.pulls == 1) != missing {
			t.Fatalf("pulls=%d, missing=%v", s.pulls, missing)
		}
		if _, err := s.lease.Stat(); err != nil {
			t.Fatal("lease closed before use")
		}
		lease.Close()
		if _, err := s.lease.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("lease did not close: %v", err)
		}
	}
}

func TestResolveExecutionFailureClosesLeaseAndLocalMissDoesNotPull(t *testing.T) {
	s := &fakeImageSource{record: image.Record{ID: "sha256:" + strings.Repeat("a", 64), Config: &image.LaunchConfig{}}, tree: t.TempDir()}
	var err error
	s.lease, err = os.CreateTemp(t.TempDir(), "lease")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveExecution(context.Background(), config.Config{Image: "redis:8"}, nil, s); err == nil {
		t.Fatal("missing command accepted")
	}
	if _, err := s.lease.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("failure leaked lease")
	}
	s.missing = true
	for _, ref := range []string{s.record.ID, "a", "aaaaaaaaaaaa", "sha256:aaaaaaaaaaaa", strings.Repeat("a", 64)} {
		if _, _, err := resolveExecution(context.Background(), config.Config{Image: ref}, nil, s); !errors.Is(err, image.ErrNotFound) || s.pulls != 0 {
			t.Fatalf("local miss %s pulled: %d %v", ref, s.pulls, err)
		}
	}
	s.resolveErr = image.ErrAmbiguousID
	if _, _, err := resolveExecution(context.Background(), config.Config{Image: "aaaaaaaaaaaa"}, nil, s); !errors.Is(err, image.ErrAmbiguousID) || s.pulls != 0 {
		t.Fatalf("ambiguous prefix pulled: %d %v", s.pulls, err)
	}
}
