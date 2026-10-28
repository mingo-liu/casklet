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

	"github.com/mingo-liu/casklet/internal/project"
)

func TestMacDeploymentHistoryAndExplicitRollback(t *testing.T) {
	client(t)
	dir := hostDirectory(t)
	file := filepath.Join(dir, "project.json")
	envfile := filepath.Join(dir, "app.env")
	name := fmt.Sprintf("history-%x", time.Now().UnixNano())
	run := map[string]any{"network": "none", "env-file": []string{"app.env"}, "mount": []string{"type=volume,source=data,target=/data"}, "command": []string{"/bin/sleep", "300"}}
	doc := map[string]any{"name": name, "networks": []string{}, "volumes": []string{"data"}, "services": map[string]any{"app": map[string]any{"run": run}}}
	save := func(version string) {
		t.Helper()
		os.WriteFile(envfile, []byte("APP_VERSION="+version+"\n"), 0600)
		data, _ := json.Marshal(doc)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	history := func() []project.DeploymentSummary {
		t.Helper()
		data := success(t, "project", "history", "--json", name)
		var h []project.DeploymentSummary
		if strings.Contains(data, "APP_VERSION") || json.Unmarshal([]byte(data), &h) != nil {
			t.Fatalf("history: %s", data)
		}
		return h
	}
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	ref := "cp-" + name + "-app"
	save("one")
	success(t, "project", "up", "--config", file, "--wait")
	success(t, "exec", ref, "--", "/bin/sh", "-c", "echo durable > /data/value")
	success(t, "project", "up", "--config", file)
	if len(history()) != 1 {
		t.Fatal("no-op revision")
	}
	save("two")
	success(t, "project", "up", "--config", file, "--recreate", "app")
	os.Remove(file)
	os.Remove(envfile)
	success(t, "project", "rollback", "--wait", name, "1")
	if got := success(t, "exec", ref, "--", "/bin/sh", "-c", "printf '%s:' \"$APP_VERSION\"; cat /data/value"); got != "one:durable\n" {
		t.Fatalf("rollback: %q", got)
	}
	if h := history(); len(h) != 3 || h[2].RestoredFrom != 1 {
		t.Fatal("provenance missing")
	}
	run["command"] = []string{"/missing-command"}
	save("broken")
	if _, _, code := command(t, "project", "up", "--config", file, "--recreate", "app"); code == 0 {
		t.Fatal("bad deployment succeeded")
	}
	success(t, "project", "rollback", name, "1")
	if got := success(t, "exec", ref, "--", "/bin/sh", "-c", "echo $APP_VERSION"); got != "one\n" {
		t.Fatalf("recovery: %q", got)
	}
	success(t, "project", "down", "--volumes", name)
	if len(history()) != 4 {
		t.Fatal("down removed history or failure added history")
	}
}
