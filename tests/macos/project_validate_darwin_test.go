//go:build darwin

package macos

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectValidateWithoutLimaOrMachineState(t *testing.T) {
	client(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CASKLET_MACHINE_STATE_DIR", "invalid-relative-state")
	file := filepath.Join(t.TempDir(), "project.json")
	if err := os.WriteFile(file, []byte(`{"name":"offline","services":{"app":{"run":{"image":"example/never-pulled:1","env":["TOKEN=offline-secret"]}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out := success(t, "project", "validate", "--config", file, "--json")
	var result struct {
		Valid bool   `json:"valid"`
		Name  string `json:"name"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || !result.Valid || result.Name != "offline" || strings.Contains(out, "offline-secret") {
		t.Fatalf("offline validation: %q", out)
	}
	if success(t, "project", "validate", "--help") != success(t, "help", "project", "validate") {
		t.Fatal("validation help forms differ")
	}
}
