//go:build darwin

package macos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/volume"
)

func TestProjectRestoredExternalVolumeAcrossOwnershipAndMigration(t *testing.T) {
	binary := client(t)
	name := fmt.Sprintf("external-%x", time.Now().UnixNano())
	migrated := name + "-new"
	volumeName := "cp-" + name + "-data"
	t.Cleanup(func() {
		command(t, "project", "down", "--volumes", name)
		command(t, "project", "down", "--volumes", migrated)
		command(t, "volume", "rm", volumeName)
	})
	file := filepath.Join(hostDirectory(t), "project.json")
	writeConfig := func(projectName, actual string, unhealthy bool) {
		t.Helper()
		run := map[string]any{"tmp-size": "32m", "shm-size": "96m", "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sleep", "300"}}
		if unhealthy {
			run["health-cmd"], run["health-interval"] = "exit 1", "50ms"
		}
		document := map[string]any{"name": projectName, "services": map[string]any{"app": map[string]any{"run": run}}}
		if actual == "" {
			document["volumes"] = []string{"data"}
		} else {
			document["external-volumes"] = map[string]string{"data": actual}
		}
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(name, "", false)
	success(t, "project", "up", "--config", file)
	success(t, "exec", "cp-"+name+"-app", "--", "/bin/sh", "-c", "printf restored > /data/message")
	success(t, "project", "down", name)
	var original volume.Record
	if err := json.Unmarshal([]byte(success(t, "volume", "inspect", volumeName)), &original); err != nil {
		t.Fatal(err)
	}
	archive := []byte(success(t, "volume", "export", volumeName))
	success(t, "volume", "rm", volumeName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "volume", "restore", volumeName)
	cmd.Stdin = bytes.NewReader(archive)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore: %v %s", err, output)
	}
	var restored volume.Record
	if err := json.Unmarshal([]byte(success(t, "volume", "inspect", volumeName)), &restored); err != nil || restored.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("restore identity: %+v %v", restored, err)
	}
	// The old managed receipt is stale: no implicit ownership is permitted.
	if _, _, code := command(t, "project", "up", "--config", file); code != 125 {
		t.Fatalf("owned restore unexpectedly adopted: %d", code)
	}
	writeConfig(name, volumeName, false)
	var first, second []container.Record
	if err := json.Unmarshal([]byte(success(t, "project", "up", "--config", file, "--json")), &first); err != nil || len(first) != 1 {
		t.Fatalf("restored up: %+v %v", first, err)
	}
	if output := success(t, "exec", "cp-"+name+"-app", "--", "/bin/sh", "-c", macTmpfsCapacityProbe+" && cat /data/message"); output != "restored" {
		t.Fatalf("data: %q", output)
	}
	if err := json.Unmarshal([]byte(success(t, "project", "up", "--config", file, "--json")), &second); err != nil || len(second) != 1 || second[0].ID != first[0].ID || second[0].Generation != first[0].Generation {
		t.Fatalf("reuse: %+v %v", second, err)
	}
	success(t, "project", "down", "--volumes", name)
	success(t, "volume", "inspect", volumeName)
	// A fresh project's receipts are absent, as on another Mac.
	writeConfig(migrated, volumeName, false)
	success(t, "project", "up", "--config", file)
	if output := success(t, "exec", "cp-"+migrated+"-app", "--", "cat", "/data/message"); output != "restored" {
		t.Fatalf("migrated data: %q", output)
	}
	success(t, "project", "down", "--volumes", migrated)
	writeConfig(migrated, "missing-"+name, false)
	if _, _, code := command(t, "project", "up", "--config", file); code != 125 {
		t.Fatalf("missing external accepted: %d", code)
	}
	if _, _, code := command(t, "network", "inspect", "cp-"+migrated+"-default"); code != 125 {
		t.Fatalf("preflight created network: %d", code)
	}
	writeConfig(migrated, volumeName, true)
	if _, _, code := command(t, "project", "up", "--wait", "--timeout", "2s", "--config", file); code != 124 {
		t.Fatalf("unhealthy deployment: %d", code)
	}
	if output := success(t, "project", "ps", "--json", migrated); output != "[]\n" {
		t.Fatalf("rollback left services: %s", output)
	}
	success(t, "volume", "inspect", volumeName)
	if _, _, code := command(t, "network", "inspect", "cp-"+migrated+"-default"); code != 125 {
		t.Fatalf("rollback left network: %d", code)
	}
	// Verify retained bytes after both down --volumes and failed startup.
	writeConfig(migrated, volumeName, false)
	success(t, "project", "up", "--config", file)
	if output := success(t, "exec", "cp-"+migrated+"-app", "--", "cat", "/data/message"); output != "restored" {
		t.Fatalf("rollback lost data: %q", output)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	success(t, "project", "down", "--volumes", migrated)
	success(t, "volume", "inspect", volumeName)
}
