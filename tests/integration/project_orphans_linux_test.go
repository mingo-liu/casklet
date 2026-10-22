//go:build linux

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestProjectRemoveOrphansPreservesRunningServicesAndData(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_MACHINE_STATE_DIR", t.TempDir())
	source := template
	name := fmt.Sprintf("orphans-%x", time.Now().UnixNano())
	file := filepath.Join(t.TempDir(), "project.json")
	services := map[string]any{
		"db":      map[string]any{"run": map[string]any{"rootfs": source, "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sleep", "300"}}},
		"api":     map[string]any{"run": map[string]any{"rootfs": source, "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sleep", "300"}}},
		"retired": map[string]any{"run": map[string]any{"rootfs": source, "command": []string{"/bin/sleep", "300"}}},
	}
	document := map[string]any{"name": name, "networks": []string{"default", "unused"}, "volumes": []string{"data", "unused"}, "services": services}
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
	t.Cleanup(func() { backgroundCLI(t, "project", "down", "--volumes", name) })
	backgroundSuccess(t, "project", "up", "--config", file, "--wait", "--timeout", "30s")
	inspect := func(key string) container.Inspection {
		t.Helper()
		var value container.Inspection
		if out := backgroundSuccess(t, "inspect", "cp-"+name+"-"+key); json.Unmarshal([]byte(out), &value) != nil {
			t.Fatalf("inspection %s", out)
		}
		return value
	}
	kept, old := inspect("db"), inspect("api")
	backgroundSuccess(t, "exec", old.ID, "--", "/bin/sh", "-c", "printf saved > /data/message; touch /private-marker")
	backgroundSuccess(t, "stop", "--timeout", "0s", "cp-"+name+"-retired")
	delete(services, "api")
	delete(services, "retired")
	worker := map[string]any{"rootfs": source, "network-alias": []string{"api"}, "command": []string{"/bin/sleep", "300"}}
	services["worker"] = map[string]any{"run": worker}
	document["networks"] = []string{"default"}
	document["volumes"] = []string{"data"}
	save()
	if code := orphanCommandCode(t, "project", "up", "--config", file); code != 125 {
		t.Fatalf("implicit removal: %d", code)
	}
	stateRoot, err := project.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(stateRoot, name+".json")
	state, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	preview := backgroundSuccess(t, "project", "up", "--config", file, "--remove-orphans", "--dry-run", "--json")
	var changes []project.Change
	if err := json.Unmarshal([]byte(preview), &changes); err != nil {
		t.Fatal(err)
	}
	removals := map[string]string{}
	for _, change := range changes {
		if change.Action == "remove" && change.Reason == "service_removed" {
			removals[change.Name] = change.ID
		}
	}
	if len(removals) != 2 || removals["api"] != old.ID || removals["retired"] == "" {
		t.Fatalf("preview %s", preview)
	}
	after, err := os.ReadFile(stateFile)
	if err != nil || !bytes.Equal(state, after) || inspect("api").ID != old.ID {
		t.Fatal("preview changed state")
	}
	worker["mount"] = []string{"type=bind,source=" + filepath.Join(filepath.Dir(file), "missing") + ",target=/missing"}
	save()
	if code := orphanCommandCode(t, "project", "up", "--config", file, "--remove-orphans"); code != 125 || inspect("api").ID != old.ID {
		t.Fatal("source failure removed old service")
	}
	delete(worker, "mount")
	save()
	backgroundSuccess(t, "project", "up", "--config", file, "--remove-orphans", "--wait", "--timeout", "30s")
	current := inspect("db")
	if current.ID != kept.ID || current.Generation != kept.Generation || current.State != container.StateRunning {
		t.Fatalf("changed retained service: %+v", current)
	}
	if got := backgroundSuccess(t, "exec", kept.ID, "--", "/bin/cat", "/data/message"); got != "saved" {
		t.Fatalf("lost data: %q", got)
	}
	if got := inspect("worker"); got.ID == old.ID || !slices.Contains(got.Config.NetworkAliases, "api") {
		t.Fatalf("replacement: %+v", got)
	}
	for _, key := range []string{"api", "retired"} {
		if code := orphanCommandCode(t, "inspect", "cp-"+name+"-"+key); code != 125 {
			t.Fatalf("orphan %s survived", key)
		}
	}
	backgroundSuccess(t, "volume", "inspect", "cp-"+name+"-unused")
	backgroundSuccess(t, "network", "inspect", "cp-"+name+"-unused")
	backgroundSuccess(t, "project", "up", "--config", file, "--remove-orphans", "--wait", "--timeout", "30s")
	if got := inspect("db"); got.ID != kept.ID || got.Generation != kept.Generation {
		t.Fatal("retry restarted retained service")
	}
	backgroundSuccess(t, "project", "down", "--volumes", name)
	if code := orphanCommandCode(t, "volume", "inspect", "cp-"+name+"-unused"); code != 125 {
		t.Fatal("explicit down lost undeclared resource ownership")
	}
}

func orphanCommandCode(t *testing.T, args ...string) int {
	t.Helper()
	code, _, _ := backgroundCLI(t, args...)
	return code
}
