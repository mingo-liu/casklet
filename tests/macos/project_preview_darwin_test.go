//go:build darwin

package macos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectPreviewExplainsDriftWithoutMutation(t *testing.T) {
	client(t)
	success(t, "doctor")
	name := fmt.Sprintf("preview-%x", time.Now().UnixNano())
	file := filepath.Join(t.TempDir(), "project.json")
	run := map[string]any{"command": []string{"/bin/sleep", "300"}, "memory": "128m", "env": []string{"TOKEN=before-preview-secret"}, "network": "none"}

	document := map[string]any{"name": name, "networks": []string{}, "services": map[string]any{"api": map[string]any{"run": run}}}
	write := func() {
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	t.Cleanup(func() { success(t, "project", "down", name) })
	success(t, "project", "up", "--config", file)
	inspect := func() container.Inspection {
		var result container.Inspection
		if err := json.Unmarshal([]byte(success(t, "inspect", "cp-"+name+"-api")), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	original := inspect()
	root, err := project.DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(root, name+".json")
	before, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"before-preview-secret", "/bin/sleep"} {
		if strings.Contains(string(before), secret) {
			t.Fatalf("raw configuration in comparison state: %s", before)
		}
	}
	run["env"], run["memory"] = []string{"TOKEN=after-preview-secret"}, "256m"
	write()
	preview := func(selected bool) project.Change {
		args := []string{"project", "up", "--config", file, "--dry-run", "--json"}
		if selected {
			args = append(args, "--recreate", "api")
		}
		output := success(t, args...)
		if strings.Contains(output, "preview-secret") || strings.Contains(output, "TOKEN") {
			t.Fatalf("private configuration in preview: %s", output)
		}
		var changes []project.Change
		if err := json.Unmarshal([]byte(output), &changes); err != nil || len(changes) != 1 {
			t.Fatalf("preview=%s err=%v", output, err)
		}
		return changes[0]
	}
	change := preview(false)
	if change.Reason != "configuration_changed" || change.Action != "recreate" || !change.RequiresRecreate || change.DetailsUnavailable || strings.Join(change.ChangedFields, ",") != "env,memory" {
		t.Fatalf("drift preview: %+v", change)
	}
	plain := success(t, "project", "up", "--config", file, "--dry-run", "--recreate", "api")
	if !strings.Contains(plain, "changed fields: env, memory") || strings.Contains(plain, "preview-secret") {
		t.Fatalf("plain preview: %s", plain)
	}
	after, err := os.ReadFile(stateFile)
	if err != nil || !bytes.Equal(before, after) || inspect().ID != original.ID || inspect().Generation != original.Generation {
		t.Fatalf("preview mutated deployment/state: %v", err)
	}
	if projectRecreateCode(t, "project", "up", "--config", file) != 125 || inspect().ID != original.ID {
		t.Fatal("actual drift was applied without --recreate")
	}
	success(t, "project", "up", "--config", file, "--recreate", "api")
	replacement := inspect()
	if replacement.ID == original.ID {
		t.Fatal("selected service was not replaced")
	}
	if change := preview(true); change.Reason != "explicit_recreate" || len(change.ChangedFields) != 0 || change.RequiresRecreate {
		t.Fatalf("same-config recreate: %+v", change)
	}
	// Simulate an older deployment with no field-comparison baseline.
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var legacy project.State
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Services = nil
	data, _ = json.Marshal(legacy)
	if err := os.WriteFile(stateFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	run["env"] = []string{"TOKEN=legacy-preview-secret"}
	write()
	if change := preview(false); !change.DetailsUnavailable || len(change.ChangedFields) != 0 {
		t.Fatalf("guessed legacy field changes: %+v", change)
	}
}
