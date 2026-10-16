package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/buildinfo"
	"github.com/mingo-liu/casklet/internal/storage"
)

func TestDoctorReportsChecksAndAdvisorySpace(t *testing.T) {
	for _, failed := range []bool{false, true} {
		report := doctorReport{Engine: func() *buildinfo.Info { i := buildinfo.Current(); return &i }(), Filesystem: &storage.Filesystem{TotalBytes: 100, AvailableBytes: 1, LowSpace: true}}
		report.check("runtime", nil)
		if failed {
			report.check("data_filesystem", errors.New("fixture filesystem unavailable"))
		}
		for _, asJSON := range []bool{true, false} {
			var out bytes.Buffer
			code := writeDoctor(report, asJSON, &out, io.Discard)
			if (code == 125) != failed {
				t.Fatalf("code=%d failed=%v", code, failed)
			}
			if asJSON {
				var got doctorReport
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.OK == failed || got.SchemaVersion != 1 || got.Filesystem == nil || !got.Filesystem.LowSpace {
					t.Fatalf("report: %s %v", out.String(), err)
				}
			} else if !strings.Contains(out.String(), "runtime") || failed && !strings.Contains(out.String(), "fixture filesystem unavailable") {
				t.Fatalf("text: %s", out.String())
			}
		}
	}
	if code := writeDoctor(doctorReport{}, true, failedLogOutput{}, io.Discard); code != 125 {
		t.Fatal("output failure succeeded")
	}
	for _, args := range [][]string{{"doctor", "--rootfs", "/template", "--json"}, {"doctor", "--json=false", "--rootfs", "/template"}} {
		if _, err := Parse(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
	}
}
