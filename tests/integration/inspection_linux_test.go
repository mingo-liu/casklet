//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/container"
)

func inspectBackground(t *testing.T, ref string) container.Inspection {
	t.Helper()
	out := backgroundSuccess(t, "inspect", ref)
	var got container.Inspection
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("inspection: %q, %v", out, err)
	}
	for _, private := range []string{"inspection-secret-value", `"env"`, `"run_path"`, `"cgroup"`, `"boot_id"`, `"error"`} {
		if strings.Contains(out, private) {
			t.Fatalf("private data in inspection: %q", out)
		}
	}
	return got
}

func statsBackground(t *testing.T, ref string) container.Statistics {
	t.Helper()
	out := backgroundSuccess(t, "stats", "--json", "--interval", "500ms", ref)
	var got container.Statistics
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stats: %q, %v", out, err)
	}
	return got
}

func TestInspectionAndStatsLifecycle(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, []string{"--memory", "64m", "--pids-limit", "24", "--cpus", "0.25", "--env", "TOKEN=inspection-secret-value", "--user", "1000:1001", "--workdir", "/tmp", "--read-only"}, "/bin/sh", "-c", "while :; do :; done")
	for _, ref := range []string{id, name} {
		got := inspectBackground(t, ref)
		if got.ID != id || got.Name != name || got.State != "running" || got.CreatedAt.IsZero() || got.StartedAt == nil || got.FinishedAt != nil || got.ExitCode != nil ||
			got.Config.User.UID != 1000 || got.Config.User.GID != 1001 || !got.Config.ReadOnly || got.Config.Workdir != "/tmp" ||
			got.Limits.MemoryBytes != 64<<20 || got.Limits.Pids != 24 || got.Limits.CPUs != 0.25 {
			t.Fatalf("active inspection: %+v", got)
		}
	}
	stats := statsBackground(t, id)
	if stats.ID != id || stats.State != "running" || stats.MemoryBytes == nil || *stats.MemoryBytes == 0 || stats.MemoryLimitBytes != 64<<20 || stats.CPUPercent == nil || *stats.CPUPercent < 1 || *stats.CPUPercent > 65 || stats.CPUUsageUsec == nil || *stats.CPUUsageUsec == 0 || stats.SampledAt.IsZero() {
		t.Fatalf("real workload metrics: %+v", stats)
	}
	interval, err := time.ParseDuration(stats.Interval)
	if err != nil || interval < 500*time.Millisecond {
		t.Fatalf("interval: %s, %v", stats.Interval, err)
	}
	table := backgroundSuccess(t, "stats", "--interval", "100ms", name)
	if !strings.Contains(table, "MEMORY (BYTES)") || strings.Contains(table, "N/A") {
		t.Fatalf("active stats table: %q", table)
	}
	backgroundSuccess(t, "stop", name)
	completed := inspectBackground(t, id)
	if completed.State != "exited" || completed.StartedAt == nil || completed.FinishedAt == nil || completed.ExitCode == nil || completed.FinishedAt.Before(*completed.StartedAt) || completed.Config.User.UID != 1000 {
		t.Fatalf("completed inspection: %+v", completed)
	}
	stats = statsBackground(t, name)
	if stats.MemoryBytes != nil || stats.CPUPercent != nil || stats.CPUUsageUsec != nil || stats.MemoryUnavailable == "" || stats.CPUUnavailable == "" {
		t.Fatalf("completed metrics must be unavailable: %+v", stats)
	}
	if out := backgroundSuccess(t, "stats", name); strings.Count(out, "N/A") != 2 {
		t.Fatalf("completed stats table: %q", out)
	}
	backgroundSuccess(t, "rm", name)
	for _, action := range []string{"inspect", "stats"} {
		code, out, stderr := backgroundCLI(t, action, id)
		if code != 125 || out != "" || !strings.Contains(stderr, "container not found") {
			t.Fatalf("removed %s: %d %q %q", action, code, out, stderr)
		}
	}
}

func TestInspectionFailedContainer(t *testing.T) {
	name := backgroundName(t)
	code, _, _ := backgroundCLI(t, detachedArguments(name, []string{"--env", "TOKEN=inspection-secret-value"}, "/bin/does-not-exist")...)
	if code != 125 {
		t.Fatalf("invalid command exit=%d", code)
	}
	waitBackground(t, name, "failed")
	got := inspectBackground(t, name)
	if got.State != "failed" || got.FinishedAt == nil {
		t.Fatalf("failed inspection: %+v", got)
	}
	stats := statsBackground(t, name)
	if stats.MemoryBytes != nil || stats.CPUPercent != nil {
		t.Fatalf("failed metrics: %+v", stats)
	}
}

func TestStatsUnavailableCgroup(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sleep", "30")
	// Private metadata corruption must neither disclose host data nor read unrelated cgroups.
	path := filepath.Join("/var/lib/mini-docker/containers", id, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record container.Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	original := record.Cgroup
	t.Cleanup(func() {
		current, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Error(err)
			return
		}
		if err := json.Unmarshal(current, &record); err != nil {
			t.Error(err)
			return
		}
		record.Cgroup = original
		restored, err := json.Marshal(record)
		if err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(path, restored, 0600); err != nil {
			t.Error(err)
		}
	})
	for _, target := range []string{"", "/etc", "/sys/fs/cgroup", filepath.Join(filepath.Dir(original), "container-000000000000000000000000")} {
		record.Cgroup = target
		changed, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, changed, 0600); err != nil {
			t.Fatal(err)
		}
		got := statsBackground(t, id)
		if got.MemoryBytes != nil || got.CPUPercent != nil || got.MemoryUnavailable == "" || got.CPUUnavailable == "" {
			t.Fatalf("unavailable cgroup %q: %+v", target, got)
		}
	}
}

func TestInspectionAndStatsConcurrentRemoval(t *testing.T) {
	for _, action := range []string{"inspect", "stats"} {
		t.Run(action, func(t *testing.T) {
			name := backgroundName(t)
			id := startBackground(t, name, nil, "/bin/sleep", "30")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			args := []string{action, id}
			if action == "stats" {
				args = []string{action, "--json", "--interval", "1s", id}
			}
			cmd := exec.CommandContext(ctx, binary, args...)
			var out, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &out, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			if action == "stats" {
				waitStatsSampling(t, cmd, id)
			}
			backgroundSuccess(t, "stop", id)
			backgroundSuccess(t, "rm", id)
			select {
			case err := <-done:
				if err == nil {
					var identity struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal([]byte(out.String()), &identity); err != nil || identity.ID != id {
						t.Fatalf("concurrent %s response: %q, %v", action, out.String(), err)
					}
				} else {
					exit, ok := err.(*exec.ExitError)
					if !ok || exit.ExitCode() != 125 || out.Len() != 0 || !strings.Contains(stderr.String(), "container not found") {
						t.Fatalf("concurrent %s: %v, %q, %q", action, err, out.String(), stderr.String())
					}
				}
			case <-ctx.Done():
				t.Fatal("concurrent removal blocked inspection/statistics")
			}
			// Reuse the name and ensure the removed ID cannot resolve to its replacement.
			replacement := startBackground(t, name, nil, "/bin/sleep", "30")
			if got := inspectBackground(t, name); got.ID != replacement {
				t.Fatalf("reused name: %+v", got)
			}
			code, _, _ := backgroundCLI(t, action, id)
			if code != 125 {
				t.Fatal("removed ID resolved to the replacement")
			}
		})
	}
}

func TestStatsIdleExitAndCancellation(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sleep", "30")
	got := statsBackground(t, id)
	if got.CPUPercent == nil || *got.CPUPercent > 10 || got.MemoryBytes == nil || *got.MemoryBytes == 0 {
		t.Fatalf("idle unlimited workload: %+v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "stats", "--json", "--interval", "5s", id)
	var out, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitStatsSampling(t, cmd, id)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 130 || out.Len() != 0 {
		t.Fatalf("canceled stats: %v, %q, %q", err, out.String(), stderr.String())
	}
	if got := inspectBackground(t, id); got.State != "running" {
		t.Fatalf("stats cancellation stopped workload: %+v", got)
	}
	cmd = exec.CommandContext(ctx, binary, "stats", "--json", "--interval", "2s", id)
	out.Reset()
	stderr.Reset()
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitStatsSampling(t, cmd, id)
	backgroundSuccess(t, "stop", id)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit during stats: %v, %q", err, stderr.String())
	}
	if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "exited" || got.CPUPercent != nil || got.MemoryBytes != nil {
		t.Fatalf("metrics after concurrent exit: %+v", got)
	}
}

// Observe the pinned workload descriptor so interruption and removal really occur
// during the sampling window rather than before the request starts.
func waitStatsSampling(t *testing.T, cmd *exec.Cmd, id string) {
	t.Helper()
	dir := fmt.Sprintf("/proc/%d/fd", cmd.Process.Pid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("statistics process exited before sampling: %v", err)
		}
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join(dir, entry.Name()))
			if err == nil && strings.Contains(target, "mini-docker-"+id+".service/container-") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("statistics request did not pin its workload cgroup")
}
