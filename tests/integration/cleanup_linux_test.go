//go:build linux

package integration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupFailureRetainsExitStatusAndRecoveryReceipt(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit-%d", code), func(t *testing.T) { testCleanupFailureRetainsExitStatus(t, code) })
	}
}

func testCleanupFailureRetainsExitStatus(t *testing.T, exitCode int) {
	t.Helper()
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", fmt.Sprintf("while [ ! -f /finish ]; do sleep 0.1; done; exit %d", exitCode))
	record := waitBackground(t, id, "running")
	if !strings.HasPrefix(record.Cgroup, "/sys/fs/cgroup/") || filepath.Base(filepath.Dir(record.Cgroup)) != "mini-docker-"+id+".service" {
		t.Fatalf("unexpected workload identity: %q", record.Cgroup)
	}
	// An empty, unrecognized child must prevent recursive cgroup deletion.
	child := filepath.Join(record.Cgroup, "preserve-unexpected-child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(child); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Error(err)
		}
	})
	backgroundSuccess(t, "exec", id, "--", "/bin/touch", "/finish")
	record = waitBackground(t, id, "exited")
	assertBackgroundExit(t, record, exitCode)
	if len(record.CleanupFailures) != 1 || record.CleanupFailures[0] != "cgroup.remove" || !strings.Contains(record.Error, "cleanup cgroup.remove") {
		t.Fatalf("cleanup failure not persisted: %+v", record)
	}
	if _, err := os.Stat(filepath.Join(record.RunPath, "state.json")); err != nil {
		t.Fatal("recovery receipt removed:", err)
	}
	inspection := inspectBackground(t, id)
	if len(inspection.CleanupFailures) != 1 || inspection.CleanupFailures[0] != "cgroup.remove" || inspection.ExitCode == nil || *inspection.ExitCode != exitCode {
		t.Fatalf("inspection did not separate command and cleanup results: %+v", inspection)
	}
	code, out, diagnostic := backgroundCLI(t, "wait", id)
	if code != exitCode || strings.TrimSpace(out) != fmt.Sprint(exitCode) || diagnostic != "" {
		t.Fatalf("wait lost command result: %d %q %q", code, out, diagnostic)
	}
	// systemd may already have removed the empty delegated tree when the
	// supervisor exited; the retained receipt still records the failed step.
	if err := os.Remove(child); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	backgroundSuccess(t, "restart", id)
	next := waitBackground(t, id, "exited")
	assertBackgroundExit(t, next, exitCode)
	inspection = inspectBackground(t, id)
	if len(inspection.CleanupFailures) != 0 || inspection.PreviousExit == nil || len(inspection.PreviousExit.CleanupFailures) != 1 {
		t.Fatalf("restart lost cleanup history: %+v", inspection)
	}
}
