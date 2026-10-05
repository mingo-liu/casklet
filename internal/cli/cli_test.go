package cli

import (
	"reflect"
	"testing"
	"time"
)

func TestParseCommandBoundaries(t *testing.T) {
	r, err := Parse([]string{"run", "--rootfs", "/tmp/rootfs", "--timeout", "2s", "--", "/bin/sh", "-c", "echo '$HOME'; exit 7"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Memory != 128<<20 || r.Config.PidsLimit != 64 || r.Config.Timeout != 2*time.Second {
		t.Fatalf("unexpected defaults: %+v", r.Config)
	}
	want := []string{"/bin/sh", "-c", "echo '$HOME'; exit 7"}
	if !reflect.DeepEqual(r.Config.Command, want) {
		t.Fatalf("command changed: %q", r.Config.Command)
	}
}

func TestParseRejectsInvalidOptions(t *testing.T) {
	cases := [][]string{
		{"run", "--rootfs", "/tmp/r", "/bin/sh"},
		{"run", "--rootfs", "/tmp/r", "--"},
		{"run", "--", "echo"},
		{"run", "--rootfs", "/tmp/r", "--hostname", "-bad", "--", "echo"},
		{"run", "--rootfs", "/tmp/r", "--pids-limit", "0", "--", "echo"},
		{"run", "--rootfs", "/tmp/r", "--timeout", "-1s", "--", "echo"},
		{"doctor", "--rootfs", "/tmp/r", "--", "echo"},
		{"unknown"},
	}
	for _, args := range cases {
		if _, err := Parse(args); err == nil {
			t.Errorf("accepted invalid arguments: %q", args)
		}
	}
}

func TestMemoryBoundaries(t *testing.T) {
	for input, want := range map[string]int64{"1": 1, "64m": 64 << 20, "2G": 2 << 30, "1024k": 1 << 20} {
		got, err := ParseMemory(input)
		if err != nil || got != want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0", "-1", "1.5m", "1mb", "8589934592g", "9223372036854775808", " 1m"} {
		if _, err := ParseMemory(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestHelpDoesNotRequireRootFS(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"run", "--help"}, {"doctor", "-h"}} {
		r, err := Parse(args)
		if err != nil || r.Action != "help" {
			t.Errorf("Parse(%q) = %+v, %v", args, r, err)
		}
	}
}
