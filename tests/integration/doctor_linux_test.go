//go:build linux

package integration

import (
	"encoding/json"
	"testing"
)

func TestDoctorJSON(t *testing.T) {
	code, out, stderr := start(t, "", "doctor", "--rootfs", template, "--json").wait(t)
	var report struct {
		OK            bool  `json:"ok"`
		SchemaVersion int   `json:"schema_version"`
		Engine        any   `json:"engine"`
		Filesystem    any   `json:"filesystem"`
		Checks        []any `json:"checks"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &report) != nil || !report.OK || report.SchemaVersion != 1 || report.Engine == nil || report.Filesystem == nil || len(report.Checks) < 2 {
		t.Fatalf("doctor: %d %s %s", code, out, stderr)
	}
	code, out, stderr = start(t, "", "doctor", "--rootfs", "/nonexistent-casklet-doctor-template", "--json").wait(t)
	if code != 125 || json.Unmarshal([]byte(out), &report) != nil || report.OK || len(report.Checks) < 2 {
		t.Fatalf("failed doctor: %d %s %s", code, out, stderr)
	}
}
