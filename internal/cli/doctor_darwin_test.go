//go:build darwin

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorJSONWithoutLimaStillReportsClient(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	state := filepath.Join(t.TempDir(), "uncreated")
	t.Setenv("CASKLET_MACHINE_STATE_DIR", state)
	out, _, code := helpExecution(t, []string{"doctor", "--json"})
	var report doctorReport
	if code != 125 || json.Unmarshal([]byte(out), &report) != nil || report.OK || report.Client == nil || report.SchemaVersion != 1 || len(report.Checks) == 0 {
		t.Fatalf("report: %d %s", code, out)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("state created: %v", err)
	}
	for _, args := range [][]string{{"doctor", "--json", "--rootfs", "/template"}, {"doctor", "--json=false", "--rootfs=/template"}} {
		normalized := platformArguments(args)
		if strings.Count(strings.Join(normalized, " "), "--rootfs") != 1 {
			t.Fatalf("rootfs ignored after json: %v", normalized)
		}
		got, err := hostPathArguments(normalized, "doctor")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.Join(got, " "), "/template") {
			t.Fatalf("path lost: %v", got)
		}
	}
}
