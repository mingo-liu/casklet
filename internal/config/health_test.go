package config

import (
	"reflect"
	"testing"
	"time"
)

func ptr[T any](value T) *T { return &value }

func TestHealthValidationAndMerge(t *testing.T) {
	defaults := &HealthConfig{Test: []string{"CMD", "/check", "argument"}, Interval: ptr(time.Second), StartPeriod: ptr(time.Minute), Retries: ptr(4)}
	merged := MergeHealth(defaults, &HealthConfig{Timeout: ptr(2 * time.Second), StartPeriod: ptr(time.Duration(0))})
	if err := merged.Validate(); err != nil {
		t.Fatal(err)
	}
	if merged.IntervalValue() != time.Second || merged.TimeoutValue() != 2*time.Second || merged.StartPeriodValue() != 0 || merged.RetriesValue() != 4 || !reflect.DeepEqual(merged.Command(), []string{"/check", "argument"}) {
		t.Fatalf("merge: %+v", merged)
	}
	merged.Test[1] = "/changed"
	if defaults.Test[1] != "/check" {
		t.Fatal("modified image defaults")
	}
	disabled := MergeHealth(defaults, &HealthConfig{Test: []string{"NONE"}})
	if disabled.Enabled() {
		t.Fatal("NONE enabled")
	}
	if (&HealthConfig{Test: []string{"CMD-SHELL", "true"}, Shell: []string{"/bin/custom", "-c"}}).Command()[0] != "/bin/custom" {
		t.Fatal("custom shell ignored")
	}
	for _, h := range []*HealthConfig{
		{Test: []string{"BAD"}}, {Test: []string{"CMD"}}, {Test: []string{"CMD", ""}}, {Test: []string{"CMD-SHELL", " "}}, {Test: []string{"CMD-SHELL", "a", "b"}}, {Test: []string{"NONE", "a"}}, {Test: []string{"CMD", "a\x00b"}},
		{Interval: ptr(time.Duration(0))}, {Timeout: ptr(-time.Second)}, {StartInterval: ptr(time.Nanosecond)}, {Interval: ptr(25 * time.Hour)}, {StartPeriod: ptr(-time.Second)}, {Retries: ptr(0)}, {Retries: ptr(1001)}, {Shell: []string{""}},
	} {
		if h.Validate() == nil {
			t.Fatalf("accepted %+v", h)
		}
	}
	cfg := Config{Command: []string{"true"}, Healthcheck: &HealthConfig{Interval: ptr(time.Second)}}
	if cfg.ValidateRunRequest() != nil || cfg.ValidateExecution() == nil {
		t.Fatal("unresolved health options accepted for execution or rejected for image request")
	}
}
