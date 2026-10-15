//go:build darwin

package macos

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersionQueriesDoNotNeedLimaOrMachineState(t *testing.T) {
	client(t)
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	t.Setenv("LIMA_HOME", filepath.Join(directory, "must-not-create"))
	t.Setenv("CASKLET_MACHINE_STATE_DIR", "invalid-relative-state")
	plain := success(t, "--version")
	if plain != success(t, "version") || !strings.Contains(plain, "Platform: darwin/"+runtime.GOARCH) {
		t.Fatalf("inconsistent native version: %q", plain)
	}
	encoded := success(t, "version", "--json")
	if encoded != success(t, "--version", "--json") {
		t.Fatal("JSON version aliases differ")
	}
	var info struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Engine  struct {
			Arch   string `json:"arch"`
			SHA256 string `json:"sha256"`
		} `json:"engine"`
	}
	if err := json.Unmarshal([]byte(encoded), &info); err != nil || info.Version == "" || len(info.Commit) != 40 || info.Engine.Arch != runtime.GOARCH || len(info.Engine.SHA256) != 64 {
		t.Fatalf("incomplete version metadata: %s %v", encoded, err)
	}
	if success(t, "version", "--help") != success(t, "help", "version") {
		t.Fatal("version help differs")
	}
	if _, err := os.Stat(filepath.Join(directory, "must-not-create")); !os.IsNotExist(err) {
		t.Fatalf("version query created machine state: %v", err)
	}
}
