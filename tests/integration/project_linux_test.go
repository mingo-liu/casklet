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
