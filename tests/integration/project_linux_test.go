//go:build linux

package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestProjectDeploymentThroughLinuxEngine(t *testing.T) {
	require(t)
	name := fmt.Sprintf("deployment-%x", time.Now().UnixNano())
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	file := filepath.Join(t.TempDir(), "project.json")
	document := map[string]any{"name": name, "volumes": []string{"data"}, "services": map[string]any{
		"db":  map[string]any{"run": map[string]any{"rootfs": template, "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sleep", "30"}, "health-cmd": "true", "health-interval": "50ms"}},
		"api": map[string]any{"depends-on": map[string]string{"db": "healthy"}, "run": map[string]any{"rootfs": template, "command": []string{"/bin/sleep", "30"}}},
	}}
	data, _ := json.Marshal(document)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backgroundCLI(t, "project", "down", "--volumes", name) })
	var records []container.Record
	output := backgroundSuccess(t, "project", "up", "--config", file, "--timeout", "2s", "--json")
	if err := json.Unmarshal([]byte(output), &records); err != nil || len(records) != 2 {
		t.Fatalf("deployment %v %s", err, output)
	}
	var metrics []container.Statistics
	output = backgroundSuccess(t, "stats", "--json", "--interval", "100ms", "--filter", "label="+project.LabelName+"="+name)
	if err := json.Unmarshal([]byte(output), &metrics); err != nil || len(metrics) != 2 || metrics[0].MemoryBytes == nil || metrics[1].CPUPercent == nil {
		t.Fatalf("project metrics %v %s", err, output)
	}
	db := ""
	for _, record := range records {
		if record.Labels[project.LabelService] == "db" {
			db = record.ID
		}
	}
	if db == "" {
		t.Fatal("missing service")
	}
	backgroundSuccess(t, "exec", db, "--", "/bin/sh", "-c", "printf saved > /data/message")
	original := records[0].ID
	output = backgroundSuccess(t, "project", "up", "--config", file, "--json")
	if err := json.Unmarshal([]byte(output), &records); err != nil || records[0].ID != original {
		t.Fatalf("reuse %v %s", err, output)
	}
	backgroundSuccess(t, "project", "down", name)
	backgroundSuccess(t, "volume", "inspect", "cp-"+name+"-data")
	backgroundSuccess(t, "project", "up", "--config", file)
	if output := backgroundSuccess(t, "exec", "cp-"+name+"-db", "--", "cat", "/data/message"); output != "saved" {
		t.Fatalf("data %q", output)
	}
	os.Remove(file)
	backgroundSuccess(t, "project", "down", "--volumes", name)
	if output := backgroundSuccess(t, "project", "ps", "--json", name); output != "[]\n" {
		t.Fatalf("retained records %q", output)
	}
}

func TestProjectWaitCoversLeafReadinessAndRollback(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	for _, ready := range []bool{true, false} {
		name := fmt.Sprintf("wait-%x", time.Now().UnixNano())
		probe := "test -e /ready"
		if !ready {
			probe = "exit 1"
		}
		document := map[string]any{"name": name, "networks": []string{}, "services": map[string]any{
			"leaf":   map[string]any{"run": map[string]any{"rootfs": template, "network": "none", "health-cmd": probe, "health-interval": "50ms", "command": []string{"/bin/sh", "-c", "sleep .3; touch /ready; exec sleep 30"}}},
			"worker": map[string]any{"run": map[string]any{"rootfs": template, "network": "none", "command": []string{"/bin/sleep", "30"}}},
		}}
		data, _ := json.Marshal(document)
		file := filepath.Join(t.TempDir(), "project.json")
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { backgroundCLI(t, "project", "down", "--volumes", name) })
		code, out, diagnostic := backgroundCLI(t, "project", "up", "--wait", "--timeout", "2s", "--config", file, "--json")
		if !ready {
			if code != 124 || out != "" {
				t.Fatalf("deadline code=%d output=%s diagnostic=%s", code, out, diagnostic)
			}
			if got := backgroundSuccess(t, "project", "ps", "--json", name); got != "[]\n" {
				t.Fatalf("rollback left %s", got)
			}
			continue
		}
		var records []container.Record
		if code != 0 || json.Unmarshal([]byte(out), &records) != nil || len(records) != 2 {
			t.Fatalf("ready code=%d output=%s diagnostic=%s", code, out, diagnostic)
		}
		for _, record := range records {
			if record.State != container.StateRunning || record.Labels[project.LabelService] == "leaf" && (record.Health == nil || record.Health.Status != container.HealthHealthy) {
				t.Fatalf("unready service %+v", record)
			}
		}
	}
}
