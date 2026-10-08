package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthFlagsAndConfig(t *testing.T) {
	args := []string{"run", "--image", "redis:8", "-d", "--health-cmd", "redis-cli ping", "--health-interval", "2s", "--health-timeout", "500ms", "--health-retries", "5", "--health-start-period", "0s", "--health-start-interval", "100ms"}
	r, err := Parse(args)
	if err != nil {
		t.Fatal(err)
	}
	h := r.Config.Healthcheck
	if !h.Enabled() || h.IntervalValue() != 2*time.Second || h.TimeoutValue() != 500*time.Millisecond || h.RetriesValue() != 5 || h.StartPeriodValue() != 0 || h.StartIntervalValue() != 100*time.Millisecond {
		t.Fatalf("parsed: %+v", h)
	}
	for _, args := range [][]string{
		{"run", "--image", "redis:8", "--health-cmd", "true"}, {"run", "-d", "--image", "redis:8", "--health-cmd", ""}, {"run", "-d", "--image", "redis:8", "--health-timeout", "0s"}, {"run", "-d", "--image", "redis:8", "--health-retries", "0"}, {"run", "-d", "--image", "redis:8", "--no-healthcheck", "--health-cmd", "true"}, {"run", "-d", "--rootfs", "/template", "--health-retries", "2", "--", "true"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	r, err = Parse([]string{"run", "-d", "--image", "redis:8", "--health-interval", "1s"})
	if err != nil || r.Config.Healthcheck.IntervalValue() != time.Second || len(r.Config.Healthcheck.Test) != 0 {
		t.Fatalf("image partial: %+v %v", r, err)
	}
	r, err = Parse([]string{"run", "-d", "--image", "redis:8", "--no-healthcheck"})
	if err != nil || r.Config.Healthcheck.Enabled() || r.Config.Healthcheck.Test[0] != "NONE" {
		t.Fatalf("disable: %+v %v", r, err)
	}
	file := filepath.Join(t.TempDir(), "run.json")
	os.WriteFile(file, []byte(`{"image":"redis:8","detach":true,"health-cmd":"true","health-interval":"1s","health-retries":"2"}`), 0600)
	r, err = Parse([]string{"run", "--config", file, "--health-retries", "3"})
	if err != nil || r.Config.Healthcheck.RetriesValue() != 3 {
		t.Fatalf("file health: %+v %v", r, err)
	}
}
