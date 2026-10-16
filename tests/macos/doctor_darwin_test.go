//go:build darwin

package macos

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDoctorStructuredReportAndFailure(t *testing.T) {
	client(t)
	out := success(t, "doctor", "--json")
	var report struct {
		SchemaVersion int  `json:"schema_version"`
		OK            bool `json:"ok"`
		Client        struct {
			Version string `json:"version"`
			Engine  any    `json:"engine"`
		} `json:"client"`
		Engine struct {
			OS string `json:"os"`
		} `json:"engine"`
		Machine struct {
			LimaVersion string `json:"lima_version"`
			VM          struct {
				Status string `json:"status"`
				Disk   int64  `json:"configured_disk_bytes"`
			} `json:"vm"`
		} `json:"machine"`
		Filesystem struct {
			Total     uint64 `json:"total_bytes"`
			Available uint64 `json:"available_bytes"`
		} `json:"filesystem"`
		Checks []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || !report.OK || report.SchemaVersion != 1 || report.Client.Version == "" || report.Client.Engine == nil || report.Engine.OS != "linux" || report.Machine.VM.Status != "Running" || report.Machine.LimaVersion == "" || report.Filesystem.Total == 0 || report.Filesystem.Available > report.Filesystem.Total {
		t.Fatalf("doctor: %s %v", out, err)
	}
	for _, check := range report.Checks {
		if !check.OK {
			t.Fatalf("failed check: %+v", check)
		}
	}
	failed, _, code := command(t, "doctor", "--json", "--rootfs", "/nonexistent-casklet-doctor-template")
	var failure struct {
		OK     bool  `json:"ok"`
		Checks []any `json:"checks"`
	}
	if code != 125 || json.Unmarshal([]byte(failed), &failure) != nil || failure.OK || len(failure.Checks) == 0 {
		t.Fatalf("failure: %d %s", code, failed)
	}
	cmd := exec.Command(client(t), "doctor", "--json")
	cmd.Env = append(os.Environ(), "PATH="+t.TempDir())
	data, err := cmd.Output()
	if err == nil || json.Unmarshal(data, &failure) != nil || failure.OK || !strings.Contains(string(data), "Lima is required") {
		t.Fatalf("no Lima: %s %v", data, err)
	}
	plain := success(t, "doctor")
	for _, part := range []string{"Client:", "Engine:", "Lima:", "VM:", "Guest data filesystem:", "All required runtime capabilities"} {
		if !strings.Contains(plain, part) {
			t.Fatalf("missing %s: %s", part, plain)
		}
	}
}
