//go:build linux

package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/mingo-liu/casklet/internal/cgroup"
)

type workloadCleanup interface {
	Path() string
	Kill() error
	WaitEmpty(context.Context) error
	OOMKilled() (bool, error)
	Close() error
}

var _ workloadCleanup = (*cgroup.Group)(nil)

func cleanupWorkload(group workloadCleanup, run *runDirectory, stderr io.Writer) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), _stopGrace)
	defer cancel()
	var result error
	if err := group.Kill(); err != nil {
		result = cleanupFailure("cgroup.kill", err)
	}
	if err := group.WaitEmpty(ctx); err != nil {
		run.keep = true
		return false, errors.Join(result, cleanupFailure("cgroup.empty", err))
	}
	oom, oomErr := group.OOMKilled()
	if oomErr != nil {
		oom = false
	}
	if oom {
		_, _ = fmt.Fprintln(stderr, "casklet: container exceeded its memory limit (OOM)")
	}
	if err := group.Close(); err != nil {
		// Keep the cgroup identity for a later recovery attempt even when empty.
		run.keep = true
		result = errors.Join(result, cleanupFailure("cgroup.remove", err))
	}
	return oom, result
}
