//go:build darwin

package macos

import (
	"strings"
	"testing"
)

func TestScopedHelpMatchesNamedHelp(t *testing.T) {
	for _, topic := range [][]string{
		{"run"}, {"exec"}, {"doctor"}, {"ps"}, {"inspect"}, {"stats"},
		{"wait"}, {"start"}, {"restart"}, {"stop"}, {"logs"}, {"rm"},
		{"image"}, {"image", "import"}, {"image", "ls"}, {"image", "rm"},
		{"machine"}, {"machine", "init"}, {"machine", "start"}, {"machine", "stop"},
		{"machine", "status"}, {"machine", "share"}, {"rootfs"},
	} {
		t.Run(strings.Join(topic, " "), func(t *testing.T) {
			scoped := success(t, append(append([]string(nil), topic...), "--help")...)
			named := success(t, append([]string{"help"}, topic...)...)
			if scoped != named {
				t.Fatalf("help forms differ: %q versus %q", scoped, named)
			}
			if !strings.Contains(scoped, "Usage:") {
				t.Fatalf("missing syntax: %q", scoped)
			}
			if topic[0] != "run" && strings.Contains(scoped, "--pids-limit") {
				t.Fatalf("unrelated run options: %q", scoped)
			}
			if topic[0] == "ps" && len(strings.Split(scoped, "\n")) > 25 {
				t.Fatalf("ps help too long: %q", scoped)
			}
		})
	}
	overview := success(t, "help")
	if strings.Contains(overview, "Run options:") || strings.Contains(overview, "--uid-map") {
		t.Fatalf("overview contains full option reference: %q", overview)
	}
}

func TestUnknownHelpTopicsAreErrors(t *testing.T) {
	for _, args := range [][]string{{"help", "unknown"}, {"help", "ps", "extra"}, {"help", "machine", "unknown"}, {"help", "image", "unknown"}} {
		out, diagnostic, code := command(t, args...)
		if code != 125 || out != "" || !strings.Contains(diagnostic, "Hint:") {
			t.Fatalf("%q: %d %q %q", args, code, out, diagnostic)
		}
	}
}

func TestWorkloadHelpFlagsRemainLiteral(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		if out := success(t, "run", "--", "/bin/sh", "-c", `printf '%s' "$1"`, "sh", flag); out != flag {
			t.Fatalf("workload help intercepted: %q", out)
		}
	}
	id := detached(t, "--", "/bin/sleep", "60")
	if out := success(t, "exec", id, "--", "/bin/sh", "-c", `printf '%s' "$1"`, "sh", "--help"); out != "--help" {
		t.Fatalf("exec help intercepted: %q", out)
	}
}
