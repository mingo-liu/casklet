//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestLoadedOfflineImageProjectPreviewAndSelectedRecreation(t *testing.T) {
	client(t)
	fixture := progressRegistryDetails(t)
	id := strings.TrimSpace(success(t, "image", "pull", fixture.References[0]))
	if _, err := guestRootCommand(t, "kill", strconv.Itoa(fixture.PID)); err != nil {
		t.Fatal(err)
	}
	archive := success(t, "image", "save", id)
	success(t, "image", "rm", id)
	output, diagnostic, code := registryInputCommand(t, archive, "image", "load")
	if code != 0 || output != id+"\n" {
		t.Fatalf("offline load %d %q %q", code, output, diagnostic)
	}
	name := fmt.Sprintf("offline-project-%x", time.Now().UnixNano())
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name); command(t, "image", "rm", id) })
	imageRun := map[string]any{"image": id, "network": "none", "command": []string{"/bin/busybox", "sleep", "300"}, "mount": []string{"type=volume,source=data,target=/data,readonly"}}
	document := map[string]any{"name": name, "networks": []string{}, "volumes": []string{"data"}, "services": map[string]any{
		"app":    map[string]any{"run": imageRun},
		"writer": map[string]any{"run": map[string]any{"network": "none", "command": []string{"/bin/sleep", "300"}, "mount": []string{"type=volume,source=data,target=/data"}}},
	}}
	file := filepath.Join(hostDirectory(t), "project.json")
	save := func() {
		t.Helper()
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	success(t, "project", "up", "--config", file, "--wait", "--timeout", "30s")
	inspect := func(key string) container.Inspection {
		t.Helper()
		var value container.Inspection
		out := success(t, "inspect", "cp-"+name+"-"+key)
		if err := json.Unmarshal([]byte(out), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	old, writer := inspect("app"), inspect("writer")
	if old.Config.Image != id || !old.Config.OCI || old.Config.Healthcheck == nil || old.Health == nil || old.Health.Status != container.HealthHealthy {
		t.Fatalf("loaded image defaults not inherited: %+v", old)
	}
	if len(old.Config.Healthcheck.Test) != 5 || old.Config.Healthcheck.Test[1] != "/bin/busybox" || old.Config.Healthcheck.Test[4] != "/fixture" {
		t.Fatalf("loaded image health defaults %+v", old.Config.Healthcheck)
	}
	success(t, "exec", writer.ID, "--", "/bin/sh", "-c", "printf archived-project-data > /data/message")
	imageRun["env"] = []string{"UPGRADE=selected"}
	save()
	type deploymentIdentity struct {
		ID, Name, State string
		Generation      uint64
		Labels          map[string]string
	}
	identities := func() map[string]deploymentIdentity {
		t.Helper()
		var records []container.Record
		data := success(t, "project", "ps", "--json", name)
		if err := json.Unmarshal([]byte(data), &records); err != nil {
			t.Fatal(err)
		}
		result := map[string]deploymentIdentity{}
		for _, record := range records {
			result[record.Labels[project.LabelService]] = deploymentIdentity{record.ID, record.Name, record.State, record.Generation, record.Labels}
		}
		return result
	}
	before := identities()
	var changes []project.Change
	previewOutput := success(t, "project", "up", "--config", file, "--recreate", "app", "--dry-run", "--json")
	if json.Unmarshal([]byte(previewOutput), &changes) != nil || len(changes) != 2 || changes[0].Action != "recreate" || changes[0].ID != old.ID || changes[1].Action != "reuse" || changes[1].ID != writer.ID {
		t.Fatalf("preview actions %s", previewOutput)
	}
	if after := identities(); !reflect.DeepEqual(after, before) {
		t.Fatalf("offline preview changed deployment identities: before=%+v after=%+v", before, after)
	}
	if previewed := inspect("app"); previewed.Health == nil || previewed.Health.Status != container.HealthHealthy {
		t.Fatalf("offline preview readiness: %+v", previewed.Health)
	}
	success(t, "project", "up", "--config", file, "--recreate", "app", "--wait", "--timeout", "30s")
	replaced, unchanged := inspect("app"), inspect("writer")
	if replaced.ID == old.ID || replaced.Config.Image != id || unchanged.ID != writer.ID || unchanged.Generation != writer.Generation || unchanged.State != container.StateRunning || replaced.Health == nil || replaced.Health.Status != container.HealthHealthy {
		t.Fatalf("offline upgrade: app=%+v writer=%+v", replaced, unchanged)
	}
	if got := success(t, "exec", replaced.ID, "--", "/bin/busybox", "sh", "-c", `printf '%s:' "$UPGRADE"; cat /data/message`); got != "selected:archived-project-data" {
		t.Fatalf("loaded default/persistent state %q", got)
	}
	success(t, "project", "down", name)
	success(t, "volume", "inspect", "cp-"+name+"-data")
}
