package machine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnoseReadsMetadataWithoutLocalStateOrMutations(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + quote(log) + "\ncase \"$2\" in\n--version) echo 'limactl version 2.2.1';;\nlist) echo '{\"name\":\"casklet-runtime\",\"status\":\"Stopped\",\"arch\":\"aarch64\",\"cpus\":4,\"memory\":4294967296,\"disk\":21474836480,\"sshConfigFile\":\"private-ssh-path\",\"config\":{\"mounts\":[{\"location\":\"private-share\"}]}}';;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	state := filepath.Join(dir, "uncreated")
	t.Setenv("CASKLET_MACHINE_STATE_DIR", state)
	got, err := Diagnose(context.Background())
	if err != nil || got.LimaVersion != "limactl version 2.2.1" || got.VM == nil || got.VM.Status != "Stopped" || got.VM.CPUs != 4 {
		t.Fatalf("diagnostics: %+v %v", got, err)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "private") {
		t.Fatalf("private paths: %s", data)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("created state: %v", err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "--tty=false\n--version\n--tty=false\nlist\n--json\n" {
		t.Fatalf("unexpected calls: %s", calls)
	}
}
