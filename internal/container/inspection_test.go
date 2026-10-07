package container

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func TestInspectionPublicConfiguration(t *testing.T) {
	cfg := config.Config{Mounts: []config.BindMount{{Source: "/srv/data", Target: "/data", ReadOnly: true}}, RootFS: "/template", Hostname: "worker", Command: []string{"/bin/sleep", "30"},
		Env: []string{"TOKEN=private-token", "TOKEN=second-secret", "EMPTY="}, Memory: 64 << 20, PidsLimit: 32,
		CPUQuota: 25000, Timeout: 30 * time.Second, User: &config.User{UID: 1000, GID: 1001}, ReadOnly: true}
	record := Record{ID: "id", Name: "worker", State: StateFailed, CreatedAt: time.Now(),
		RunPath: "/private-run", Cgroup: "/private-cgroup", BootID: "private-boot", Error: "private-error", CleanupFailures: []string{"cgroup.remove"}}
	got := inspectRecord(record, cfg)
	if len(got.CleanupFailures) != 1 || got.CleanupFailures[0] != "cgroup.remove" {
		t.Fatal("inspection omitted cleanup failures")
	}
	got.CleanupFailures[0] = "run.remove"
	if record.CleanupFailures[0] != "cgroup.remove" {
		t.Fatal("inspection aliases cleanup failures")
	}
	if got.Config.Workdir != "/" || got.Config.User.UID != 1000 || got.Config.Timeout != "30s" ||
		got.Limits.CPUs != 0.25 || got.Limits.MemoryBytes != 64<<20 || got.Limits.CPUPeriodUsec != 100000 {
		t.Fatalf("incorrect public configuration: %+v", got)
	}
	if len(got.Config.Mounts) != 1 || got.Config.Mounts[0] != cfg.Mounts[0] {
		t.Fatalf("inspection mounts=%+v", got.Config.Mounts)
	}
	got.Config.Mounts[0].Source = "/changed"
	if cfg.Mounts[0].Source != "/srv/data" {
		t.Fatal("inspection aliases persisted mount configuration")
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-token", "second-secret", "private-run", "private-cgroup", "private-boot", "private-error", "run_path", "boot_id", "cpu_usage_usec"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("inspection disclosed %q: %s", secret, data)
		}
	}
	if strings.Join(got.Config.EnvironmentNames, ",") != "PATH,HOME,LANG,TOKEN,EMPTY" {
		t.Fatalf("environment names: %v", got.Config.EnvironmentNames)
	}
	defaults := inspectRecord(record, config.Config{Command: []string{"sh"}})
	if defaults.Config.User != (config.User{}) || defaults.Config.Timeout != "0s" {
		t.Fatalf("defaults: %+v", defaults)
	}
}

func TestCPUPercent(t *testing.T) {
	before, after := uint64(100), uint64(500100)
	if got := cpuPercent(&before, &after, time.Second); got == nil || math.Abs(*got-50) > 0.001 {
		t.Fatalf("percent: %v", got)
	}
	if got := cpuPercent(&before, &after, 250*time.Millisecond); got == nil || *got != 200 {
		t.Fatalf("multicore percent: %v", got)
	}
	if got := cpuPercent(&before, &before, time.Second); got == nil || *got != 0 {
		t.Fatal("zero usage must be available")
	}
	for _, got := range []*float64{cpuPercent(nil, &after, time.Second), cpuPercent(&before, nil, time.Second), cpuPercent(&after, &before, time.Second), cpuPercent(&before, &after, 0)} {
		if got != nil {
			t.Fatal("invalid counters should be unavailable")
		}
	}
}
