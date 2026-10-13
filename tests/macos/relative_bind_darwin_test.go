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
)

func TestConfigRelativeBindSourcesThroughMacTransport(t *testing.T) {
	client(t)
	dir := hostDirectory(t)
	dataDir := filepath.Join(dir, "data")
	if err := os.Mkdir(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "message"), []byte("relative-bind\n"), 0644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "run.json")
	run := map[string]any{"mount": []string{"type=bind,source=./data,target=/data,readonly"}, "command": []string{"/bin/cat", "/data/message"}}
	data, _ := json.Marshal(run)
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if out := success(t, "run", "--config", file); out != "relative-bind\n" {
		t.Fatal(out)
	}
	name := fmt.Sprintf("relative-%x", time.Now().UnixNano())
	run["command"] = []string{"/bin/sleep", "300"}
	doc, _ := json.Marshal(map[string]any{"name": name, "services": map[string]any{"app": map[string]any{"run": run}}})
	if err := os.WriteFile(file, doc, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	success(t, "project", "up", "--config", file, "--wait", "--timeout", "1m")
	ref := "cp-" + name + "-app"
	if out := success(t, "exec", ref, "--", "/bin/cat", "/data/message"); out != "relative-bind\n" {
		t.Fatal(out)
	}
	if _, diagnostic, code := command(t, "exec", ref, "--", "/bin/touch", "/data/forbidden"); code == 0 || !strings.Contains(diagnostic, "Read-only") {
		t.Fatalf("readonly bind: %d %s", code, diagnostic)
	}
	success(t, "restart", ref)
	if out := success(t, "exec", ref, "--", "/bin/cat", "/data/message"); out != "relative-bind\n" {
		t.Fatal(out)
	}
}
