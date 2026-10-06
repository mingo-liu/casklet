package config

import (
	"math"
	"reflect"
	"testing"
)

func TestCommandEnvironment(t *testing.T) {
	t.Setenv("HOST_SECRET", "never-copy")
	cfg := Config{Env: []string{"COLOR=blue", "PATH=/custom", "COLOR=red", "EMPTY=", "EQUAL=a=b", "HOME=/work"}}
	want := []string{"PATH=/custom", "HOME=/work", "LANG=C", "COLOR=red", "EMPTY=", "EQUAL=a=b"}
	for range 3 {
		got := cfg.CommandEnvironment()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("environment = %q; want %q", got, want)
		}
		got[0] = "PATH=changed"
	}
	if got := (Config{}).CommandEnvironment(); !reflect.DeepEqual(got, []string{"PATH=/bin:/usr/bin", "HOME=/", "LANG=C"}) {
		t.Fatalf("default environment = %q", got)
	}
}

func TestWorkingDirectory(t *testing.T) {
	for input, want := range map[string]string{"": "/", "/": "/", "/work/../tmp/./": "/tmp"} {
		if got := (Config{Workdir: input}).WorkingDirectory(); got != want {
			t.Errorf("WorkingDirectory(%q) = %q; want %q", input, got, want)
		}
	}
}

func TestValidateExecution(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"missing command", Config{}},
		{"empty executable", Config{Command: []string{""}}},
		{"NUL argument", Config{Command: []string{"echo", "a\x00b"}}},
		{"missing environment equals", Config{Command: []string{"echo"}, Env: []string{"KEY"}}},
		{"invalid environment key", Config{Command: []string{"echo"}, Env: []string{"9KEY=value"}}},
		{"empty environment key", Config{Command: []string{"echo"}, Env: []string{"=value"}}},
		{"NUL environment", Config{Command: []string{"echo"}, Env: []string{"KEY=a\x00b"}}},
		{"relative workdir", Config{Command: []string{"echo"}, Workdir: "tmp"}},
		{"NUL workdir", Config{Command: []string{"echo"}, Workdir: "/a\x00b"}},
		{"negative quota", Config{Command: []string{"echo"}, CPUQuota: -1}},
		{"quota below kernel minimum", Config{Command: []string{"echo"}, CPUQuota: 999}},
		{"quota exceeds maximum", Config{Command: []string{"echo"}, CPUQuota: MaxCPUQuota + 1}},
		{"reserved UID", Config{Command: []string{"echo"}, User: &User{UID: math.MaxUint32}}},
		{"reserved GID", Config{Command: []string{"echo"}, User: &User{GID: math.MaxUint32}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.ValidateExecution(); err == nil {
				t.Fatalf("accepted invalid execution options: %+v", tt.cfg)
			}
		})
	}
	valid := []Config{
		{Command: []string{"echo", ""}},
		{Command: []string{"echo"}, CPUQuota: 1000, Env: []string{"_KEY9=", "KEY=one=two"}, Workdir: "/work/../tmp"},
		{Command: []string{"echo"}, CPUQuota: MaxCPUQuota, User: &User{UID: math.MaxUint32 - 1, GID: math.MaxUint32 - 1}},
	}
	for _, cfg := range valid {
		if err := cfg.ValidateExecution(); err != nil {
			t.Fatalf("rejected valid execution options %+v: %v", cfg, err)
		}
	}
}
