//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func macWaitConfig(t *testing.T, name string, ready bool) string {
	t.Helper()
	probe := "test -e /ready"
	if !ready {
		probe = "exit 1"
	}
	document := map[string]any{"name": name, "networks": []string{}, "services": map[string]any{
		"leaf":   map[string]any{"run": map[string]any{"network": "none", "health-cmd": probe, "health-interval": "50ms", "health-retries": 1, "command": []string{"/bin/sh", "-c", "sleep .5; touch /ready; exec sleep 300"}}},
		"worker": map[string]any{"run": map[string]any{"network": "none", "command": []string{"/bin/sleep", "300"}}},
	}}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(hostDirectory(t), "project.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestProjectWaitIncludesLeafAndRunningServices(t *testing.T) {
	client(t)
	name := fmt.Sprintf("wait-%x", time.Now().UnixNano())
	file := macWaitConfig(t, name, true)
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	var records []container.Record
	data := success(t, "project", "up", "--wait", "--timeout", "20s", "--config", file, "--json")
	if err := json.Unmarshal([]byte(data), &records); err != nil || len(records) != 2 {
		t.Fatalf("records=%s err=%v", data, err)
	}
	for _, record := range records {
		if record.State != container.StateRunning {
			t.Fatalf("service not running: %+v", record)
		}
		if strings.HasSuffix(record.Name, "-leaf") && (record.Health == nil || record.Health.Status != container.HealthHealthy) {
			t.Fatalf("leaf not ready: %+v", record)
		}
	}
	var reused []container.Record
	if err := json.Unmarshal([]byte(success(t, "project", "up", "--wait", "--timeout", "20s", "--config", file, "--json")), &reused); err != nil || len(reused) != 2 {
		t.Fatal("reuse failed", err)
	}
	for i := range records {
		if records[i].ID != reused[i].ID || records[i].Generation != reused[i].Generation {
			t.Fatal("wait recreated services")
		}
	}
}

func TestProjectWaitDeadlineRollsBackNewAndPreservesReusedServices(t *testing.T) {
	client(t)
	name := fmt.Sprintf("wait-fail-%x", time.Now().UnixNano())
	file := macWaitConfig(t, name, false)
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	if out, diagnostic, code := command(t, "project", "up", "--wait", "--timeout", "3s", "--config", file); code != 124 || out != "" || !strings.Contains(diagnostic, "project readiness") {
		t.Fatalf("deadline code=%d output=%s diagnostic=%s", code, out, diagnostic)
	}
	if got := success(t, "project", "ps", "--json", name); got != "[]\n" {
		t.Fatalf("rollback left services: %s", got)
	}
	var original []container.Record
	if err := json.Unmarshal([]byte(success(t, "project", "up", "--config", file, "--json")), &original); err != nil || len(original) != 2 {
		t.Fatal("default startup failed", err)
	}
	if _, _, code := command(t, "project", "up", "--wait", "--timeout", "2s", "--config", file); code != 124 {
		t.Fatalf("reused deadline=%d", code)
	}
	var reused []container.Record
	if err := json.Unmarshal([]byte(success(t, "project", "ps", "--json", name)), &reused); err != nil || len(reused) != 2 {
		t.Fatal("reused services removed", err)
	}
	for i := range original {
		if original[i].ID != reused[i].ID || original[i].Generation != reused[i].Generation || reused[i].State != container.StateRunning {
			t.Fatalf("reused services changed: %+v", reused)
		}
	}
}
