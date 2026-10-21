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

func TestProjectBatchReservationsRejectMacPortsAndAliasesBeforeReplacement(t *testing.T) {
	client(t)
	name := fmt.Sprintf("batch-%x", time.Now().UnixNano())
	file := filepath.Join(t.TempDir(), "project.json")
	run := map[string]any{"command": []string{"/bin/sleep", "300"}}
	document := map[string]any{"name": name, "services": map[string]any{"app": map[string]any{"run": run}}}
	save := func() {
		t.Helper()
		data, _ := json.Marshal(document)
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	t.Cleanup(func() { success(t, "project", "down", name) })
	success(t, "project", "up", "--config", file)
	inspect := func() container.Inspection {
		t.Helper()
		var value container.Inspection
		if err := json.Unmarshal([]byte(success(t, "inspect", "cp-"+name+"-app")), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := inspect()
	port := fmt.Sprintf("127.0.0.1:%d:80", freePort(t))
	detached(t, "--network", "cp-"+name+"-default", "--name", "outside-"+name, "--network-alias", "occupied", "--publish", port, "--", "/bin/sleep", "300")
	for _, kind := range []string{"port", "alias"} {
		if kind == "port" {
			run["publish"] = []string{port}
		} else {
			delete(run, "publish")
			run["network-alias"] = []string{"occupied"}
		}
		save()
		for _, dryRun := range []bool{true, false} {
			args := []string{"project", "up", "--config", file, "--recreate", "app"}
			if dryRun {
				args = append(args, "--dry-run")
			}
			out, diagnostic, code := command(t, args...)
			if code != 125 || out != "" || !strings.Contains(diagnostic, kind) || !strings.Contains(diagnostic, "container") {
				t.Fatalf("%s dry-run=%v code=%d output=%s diagnostic=%s", kind, dryRun, code, out, diagnostic)
			}
			current := inspect()
			if current.ID != original.ID || current.Generation != original.Generation || current.State != container.StateRunning {
				t.Fatalf("reservation failure changed the selected execution: %+v", current)
			}
		}
	}
}
