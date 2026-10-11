package machine

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMachineStateDirectoryIsolation(t *testing.T) {
	t.Setenv("CASKLET_MACHINE_STATE_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := stateDirectory(); err != nil || got != filepath.Join(home, "Library", "Application Support", "casklet") {
		t.Fatalf("default state: %q, %v", got, err)
	}
	isolated := t.TempDir()
	t.Setenv("CASKLET_MACHINE_STATE_DIR", isolated)
	if got, err := stateDirectory(); err != nil || got != isolated {
		t.Fatalf("isolated state: %q, %v", got, err)
	}
	t.Setenv("CASKLET_MACHINE_STATE_DIR", "relative/state")
	if _, err := stateDirectory(); err == nil {
		t.Fatal("accepted relative state directory")
	}
}
