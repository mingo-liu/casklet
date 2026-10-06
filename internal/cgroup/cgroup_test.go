package cgroup

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestUnifiedPath(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"scope", "0::/system.slice/run-123.scope\n", "/system.slice/run-123.scope"},
		{"hybrid", "4:memory:/old\n0::/system.slice/run.scope\n", "/system.slice/run.scope"},
		{"colon in path", "0::/system.slice/run:a.scope\n", "/system.slice/run:a.scope"},
		{"root", "0::/\n", ""},
		{"relative", "0::system.slice/run.scope\n", ""},
		{"traversal", "0::/system.slice/../run.scope\n", ""},
		{"empty", "0::\n", ""},
		{"v1", "4:memory:/old\n", ""},
		{"duplicate", "0::/first\n0::/second\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := unifiedPath(tc.input)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unsafe membership accepted: %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestExclusiveProcess(t *testing.T) {
	if err := exclusiveProcess("42\n", 42); err != nil {
		t.Fatal(err)
	}
	for _, procs := range []string{"", "41\n", "42\n43\n", "42\n42\n"} {
		if err := exclusiveProcess(procs, 42); err == nil {
			t.Fatalf("shared scope accepted: %q", procs)
		}
	}
}

func TestParseCounters(t *testing.T) {
	got, err := parseCounters("low 0\nmax 9\noom 2\noom_kill 1\n")
	want := map[string]uint64{"low": 0, "max": 9, "oom": 2, "oom_kill": 1}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, %v; want %v", got, err, want)
	}
	for _, data := range []string{"populated", "populated 1 2", "oom_kill -1", "oom_kill infinity", "populated 1\npopulated 0"} {
		if _, err := parseCounters(data); err == nil {
			t.Fatalf("malformed counters accepted: %q", data)
		}
	}
}

func TestConfigureLimits(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"memory.max", "memory.swap.max", "memory.oom.group", "pids.max"} {
		putControl(t, dir, name, "max")
	}
	if err := configureLimits(dir, 67108864, 32, 0, writeControl); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"memory.max": "67108864", "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": "32"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s: got %q, %v; want %q", name, data, err, want)
		}
	}
}

func TestConfigureLimitsRejectsMissingController(t *testing.T) {
	dir := t.TempDir()
	putControl(t, dir, "memory.max", "max")
	if err := configureLimits(dir, 1024, 4, 0, writeControl); err == nil {
		t.Fatal("missing swap controller accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "memory.swap.max")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing kernel control was created: %v", err)
	}
	called := false
	if err := configureLimits(dir, 0, 4, 0, func(string, string) error { called = true; return nil }); err == nil || called {
		t.Fatal("invalid limits attempted a write")
	}
}

func TestConfigureCPUQuota(t *testing.T) {
	for _, quota := range []int64{1000, 25000, 100000, 150000, 100000000} {
		t.Run(fmt.Sprintf("quota_%d", quota), func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"memory.max", "memory.swap.max", "memory.oom.group", "pids.max", "cpu.max"} {
				putControl(t, dir, name, "max")
			}
			if err := configureLimits(dir, 1<<20, 16, quota, writeControl); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
			if want := fmt.Sprintf("%d 100000", quota); err != nil || string(data) != want {
				t.Fatalf("CPU bandwidth: got %q, %v; want %q", data, err, want)
			}
		})
	}
}

func TestCPUQuotaZeroPreservesDefault(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"memory.max", "memory.swap.max", "memory.oom.group", "pids.max", "cpu.max"} {
		putControl(t, dir, name, "max 100000")
	}
	if err := configureLimits(dir, 1<<20, 16, 0, writeControl); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
	if err != nil || string(data) != "max 100000" {
		t.Fatalf("unlimited CPU was changed: %q, %v", data, err)
	}
}

func TestInvalidCPUQuotaNeverWrites(t *testing.T) {
	for _, quota := range []int64{-1, math.MinInt64, 1, 999, 100000001, math.MaxInt64} {
		called := false
		err := configureLimits("unused", 1<<20, 16, quota, func(string, string) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("invalid CPU quota %d: error=%v, writes=%v", quota, err, called)
		}
	}
}

func TestMissingCPUControlNeverCreatesFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"memory.max", "memory.swap.max", "memory.oom.group", "pids.max"} {
		putControl(t, dir, name, "max")
	}
	if err := configureLimits(dir, 1<<20, 16, 25000, writeControl); err == nil || !strings.Contains(err.Error(), "cpu.max") {
		t.Fatalf("missing CPU control was not diagnosed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "cpu.max")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing CPU control was created: %v", err)
	}
}

func TestOptionalCPUController(t *testing.T) {
	for _, tc := range []struct {
		controllers string
		quota       int64
		missing     string
	}{
		{"memory pids", 0, ""},
		{"cpu memory pids", 25000, ""},
		{"pids\nmemory\ncpu\n", 25000, ""},
		{"memory pids", 25000, "cpu"},
		{"cpu pids", 0, "memory"},
		{"cpu memory", 25000, "pids"},
	} {
		err := checkControllers(tc.controllers, tc.quota)
		if tc.missing == "" {
			if err != nil {
				t.Fatalf("unexpected controller error: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), tc.missing) {
			t.Fatalf("missing %s was not diagnosed: %v", tc.missing, err)
		}
	}
}

func TestGroupControlsAndOOM(t *testing.T) {
	dir := t.TempDir()
	g := &Group{path: dir}
	putControl(t, dir, "cgroup.procs", "")
	putControl(t, dir, "cgroup.kill", "")
	if err := g.Add(-1); err == nil {
		t.Fatal("invalid PID accepted")
	}
	if err := g.Add(123); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "cgroup.procs")); string(data) != "123" {
		t.Fatalf("unexpected cgroup membership write: %q", data)
	}
	if err := g.Kill(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "cgroup.kill")); string(data) != "1" {
		t.Fatalf("unexpected cgroup kill write: %q", data)
	}
	for _, tc := range []struct {
		data string
		want bool
	}{
		{"oom 1\noom_kill 0\n", false},
		{"oom_kill 1\n", true},
		{"oom_kill 0\noom_group_kill 1\n", true},
	} {
		putControl(t, dir, "memory.events", tc.data)
		got, err := g.OOMKilled()
		if err != nil || got != tc.want {
			t.Fatalf("OOM result for %q: got %v, %v", tc.data, got, err)
		}
	}
	putControl(t, dir, "memory.events", "oom 1\n")
	if _, err := g.OOMKilled(); err == nil {
		t.Fatal("missing oom_kill counter accepted")
	}
}

func TestWaitEmpty(t *testing.T) {
	g := &Group{path: t.TempDir()}
	if err := g.WaitEmpty(context.Background()); err == nil {
		t.Fatal("unbounded wait accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	putControl(t, g.path, "cgroup.events", "populated 0\nfrozen 0\n")
	if err := g.WaitEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	putControl(t, g.path, "cgroup.events", "populated 1\nfrozen 0\n")
	short, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if err := g.WaitEmpty(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("populated cgroup did not respect deadline: %v", err)
	}
	for _, data := range []string{"populated 2\n", "frozen 0\n"} {
		putControl(t, g.path, "cgroup.events", data)
		if err := g.WaitEmpty(ctx); err == nil {
			t.Fatalf("invalid populated counter accepted: %q", data)
		}
	}
}

func TestCloseIsIdempotentAndNeverDeletesPopulatedDirectories(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "container")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	g := &Group{path: dir}
	putControl(t, dir, "unexpected", "keep")
	if err := g.Close(); err == nil {
		t.Fatal("nonempty ordinary directory removed")
	}
	if err := os.Remove(filepath.Join(dir, "unexpected")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func putControl(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
