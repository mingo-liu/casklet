//go:build linux

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCPUQuota(t *testing.T) {
	call := start(t, "", "run", "--rootfs", template, "--cpus", "0.25", "--timeout", "10s", "--", "/bin/integration-helper", "cpu")
	call.supervisor(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	control, err := exec.CommandContext(ctx, "systemctl", "show", "--property=ControlGroup", "--value", call.unit).Output()
	if err != nil {
		t.Fatalf("read CPU workload scope: %v", err)
	}
	relative := strings.TrimSpace(string(control))
	if !strings.HasPrefix(relative, "/") || filepath.Clean(relative) != relative || relative == "/" {
		t.Fatalf("invalid CPU workload scope path: %q", relative)
	}
	leaves, err := filepath.Glob(filepath.Join("/sys/fs/cgroup"+relative, "container-*"))
	if err != nil || len(leaves) != 1 {
		t.Fatalf("find CPU workload leaf: %v, %v", leaves, err)
	}
	quota, err := os.ReadFile(filepath.Join(leaves[0], "cpu.max"))
	if err != nil || strings.TrimSpace(string(quota)) != "25000 100000" {
		t.Fatalf("CPU quota: got %q, %v; expected 25000 100000", quota, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var throttled uint64
	for time.Now().Before(deadline) {
		stats, err := os.ReadFile(filepath.Join(leaves[0], "cpu.stat"))
		if err != nil {
			t.Fatalf("read CPU throttling statistics: %v", err)
		}
		for _, line := range strings.Split(string(stats), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "nr_throttled" {
				throttled, err = strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					t.Fatalf("invalid CPU throttling count: %q", line)
				}
			}
		}
		if throttled > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if throttled == 0 {
		t.Fatal("CPU workload was never throttled by the kernel")
	}
	code, out, stderr := call.wait(t)
	if code != 0 {
		t.Fatalf("CPU workload exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	var elapsedUS, cpuUS int64
	for _, field := range strings.Fields(out) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || (key != "elapsed_us" && key != "cpu_us") {
			continue
		}
		measurement, err := strconv.ParseInt(value, 10, 64)
		if err != nil || measurement <= 0 {
			t.Fatalf("invalid CPU workload measurement: %q", field)
		}
		if key == "elapsed_us" {
			elapsedUS = measurement
		} else {
			cpuUS = measurement
		}
	}
	if elapsedUS < 3000000 || cpuUS <= 0 {
		t.Fatalf("missing or too short CPU workload measurement: %q", out)
	}
	// Kernel throttling is the primary evidence. Leave room for scheduling and
	// measurement noise while rejecting a workload consuming a full CPU.
	if ratio := float64(cpuUS) / float64(elapsedUS); ratio >= 0.60 {
		t.Fatalf("CPU usage exceeds a quarter-CPU quota: ratio=%.3f stdout=%q", ratio, out)
	}
	t.Logf("quarter-CPU quota: elapsed_us=%d cpu_us=%d throttled_periods=%d", elapsedUS, cpuUS, throttled)
}
