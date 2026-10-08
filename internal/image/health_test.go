package image

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestImageHealthExtensionMergeAndIdentity(t *testing.T) {
	raw := []byte(`{"config":{"Healthcheck":{"Test":["CMD-SHELL","test -f ready"],"Interval":2000000000,"Timeout":1000000000,"StartPeriod":4000000000,"StartInterval":100000000,"Retries":2}}}`)
	health, err := imageHealthcheck(raw)
	if err != nil {
		t.Fatal(err)
	}
	if health.StartIntervalValue() != 100*time.Millisecond || health.StartPeriodValue() != 4*time.Second {
		t.Fatalf("extension: %+v", health)
	}
	defaults := LaunchConfig{Cmd: []string{"/server"}, Healthcheck: health, Shell: []string{"/bin/custom-sh", "-c"}}
	interval := time.Second
	zero := time.Duration(0)
	cfg, err := defaults.Apply(config.Config{Healthcheck: &config.HealthConfig{Interval: &interval, StartPeriod: &zero}}, t.TempDir(), nil)
	if err != nil || cfg.Healthcheck.StartPeriodValue() != 0 || cfg.Healthcheck.TimeoutValue() != time.Second || !reflect.DeepEqual(cfg.Healthcheck.Command(), []string{"/bin/custom-sh", "-c", "test -f ready"}) {
		t.Fatalf("apply: %+v %v", cfg, err)
	}
	disabled, err := defaults.Apply(config.Config{Healthcheck: &config.HealthConfig{Test: []string{"NONE"}}}, t.TempDir(), nil)
	if err != nil || disabled.Healthcheck.Enabled() {
		t.Fatalf("disabled: %+v %v", disabled, err)
	}
	if _, err := (LaunchConfig{Cmd: []string{"/server"}}).Apply(config.Config{Healthcheck: &config.HealthConfig{Interval: &interval}}, t.TempDir(), nil); err == nil {
		t.Fatal("accepted options without test")
	}
	tree := t.TempDir()
	os.WriteFile(filepath.Join(tree, "app"), []byte("same"), 0755)
	before, _, err := Identity(context.Background(), tree, "arm64", &defaults)
	if err != nil {
		t.Fatal(err)
	}
	defaults.Healthcheck = config.MergeHealth(health, &config.HealthConfig{Interval: &interval})
	after, _, err := Identity(context.Background(), tree, "arm64", &defaults)
	if err != nil || before == after {
		t.Fatalf("health ignored by identity: %s %s %v", before, after, err)
	}
	encoded, _ := json.Marshal(defaults)
	var restored LaunchConfig
	if json.Unmarshal(encoded, &restored) != nil || restored.Healthcheck.IntervalValue() != time.Second {
		t.Fatal("lost saved health")
	}
	// New omitted fields preserve image identity serialization for old images.
	legacy, _ := json.Marshal(LaunchConfig{Cmd: []string{"/server"}})
	if string(legacy) != `{"cmd":["/server"]}` {
		t.Fatalf("legacy defaults changed: %s", legacy)
	}
	for _, raw := range []string{`{"config":{"Healthcheck":{"Test":["NONE"]}}}`, `{"config":{"Healthcheck":{"Test":[]}}}`, `{"config":{}}`} {
		h, err := imageHealthcheck([]byte(raw))
		if err != nil || h.Enabled() {
			t.Fatalf("empty/none: %+v %v", h, err)
		}
	}
	for _, raw := range []string{`{"config":{"Healthcheck":{"Test":["BAD"]}}}`, `{"config":{"Healthcheck":{"Test":["CMD","x"],"Timeout":-1}}}`, `{"config":{"Healthcheck":{"Test":["CMD","x"],"StartInterval":1}}}`} {
		if _, err := imageHealthcheck([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
