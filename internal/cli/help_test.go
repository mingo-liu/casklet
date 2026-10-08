package cli

import (
	"os"
	"runtime"
	"strings"
	"testing"
)

func helpExecution(t *testing.T, args []string) (string, string, int) {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	diagnostic, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer diagnostic.Close()
	code := Execute(args, os.Stdin, out, diagnostic)
	output, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	errors, err := os.ReadFile(diagnostic.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(output), string(errors), code
}

func TestScopedHelpWorksWithoutRuntimeAndMatchesNamedHelp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, topic := range []string{
		"run", "exec", "doctor", "ps", "inspect", "stats", "wait", "start", "restart", "stop", "logs", "rm",
		"volume", "volume create", "volume ls", "volume inspect", "volume rm", "volume export", "volume restore",
		"system", "system df", "image prune",
		"image", "image pull", "image import", "image ls", "image rm", "help",
		"machine", "machine init", "machine start", "machine stop", "machine status", "machine share", "rootfs",
	} {
		t.Run(topic, func(t *testing.T) {
			parts := strings.Fields(topic)
			named := append([]string{"help"}, parts...)
			for _, flag := range []string{"-h", "--help"} {
				scoped := append(append([]string(nil), parts...), flag)
				r, err := Parse(scoped)
				if err != nil || r.Action != "help" || r.HelpTopic != topic {
					t.Fatalf("Parse(%q): %+v %v", scoped, r, err)
				}
				out, diagnostic, code := helpExecution(t, scoped)
				if runtime.GOOS != "darwin" && hostHelpTopic(topic) {
					if code != 125 || out != "" || !strings.Contains(diagnostic, "available only on macOS") {
						t.Fatalf("host help lacks platform error: %d %q %q", code, out, diagnostic)
					}
					continue
				}
				if code != 0 || diagnostic != "" || !strings.HasPrefix(out, "Usage: casklet "+parts[0]) || !strings.Contains(out, "Examples:") {
					t.Fatalf("scoped help: %d %q %q", code, out, diagnostic)
				}
				namedOut, namedDiagnostic, namedCode := helpExecution(t, named)
				if namedCode != 0 || namedDiagnostic != "" || namedOut != out {
					t.Fatalf("named help differs: %d %q %q", namedCode, namedOut, namedDiagnostic)
				}
				if topic != "run" && strings.Contains(out, "--pids-limit") {
					t.Fatalf("unrelated run options: %q", out)
				}
			}
		})
	}
}

func TestHelpOverviewAndShortTopicsAreFocused(t *testing.T) {
	for _, macOS := range []bool{false, true} {
		text, err := scopedUsage("", macOS)
		if err != nil || !strings.HasPrefix(text, "Usage: casklet COMMAND") || strings.Count(text, "\n") > 30 || strings.Contains(text, "--uid-map") || !strings.Contains(text, "COMMAND --help") {
			t.Fatalf("verbose or incomplete overview: %q %v", text, err)
		}
		ps, err := scopedUsage("ps", macOS)
		if err != nil || strings.Count(ps, "\n") > 25 || !strings.Contains(ps, "--all") || !strings.Contains(ps, "--json") {
			t.Fatalf("verbose or incomplete ps help: %q %v", ps, err)
		}
		run, err := scopedUsage("run", macOS)
		for _, detail := range []string{"-- COMMAND", "--uid-map", "--gid-map", "--rootless", "--stop-signal", "--restart", "--config", "--env-file", "0.01-1000", "1 KiB-64 MiB", "0s-1m", "up to 32", "Terminal options require a foreground run"} {
			if err != nil || !strings.Contains(run, detail) {
				t.Errorf("run help missing %q: %q %v", detail, run, err)
			}
		}
		if macOS && (!strings.Contains(run, "builtin:busybox") || !strings.Contains(run, "casklet run -it -- /bin/sh") || !strings.Contains(run, "Lima 2.0+")) {
			t.Fatalf("Mac run help lacks usable defaults or requirements: %q", run)
		}
		doctor, err := scopedUsage("doctor", macOS)
		want := "casklet doctor --rootfs DIRECTORY"
		if macOS {
			want = "casklet doctor [--rootfs DIRECTORY]"
		}
		if err != nil || !strings.Contains(doctor, want) || strings.Contains(doctor, "--pids-limit") {
			t.Fatalf("doctor help has wrong platform syntax: %q %v", doctor, err)
		}
	}
}

func TestInvalidHelpTopicsAndExtraArgumentsFailWithoutRuntime(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{
		{"help", "unknown"}, {"help", ""}, {"help", "ps", "extra"},
		{"help", "image", "unknown"}, {"help", "machine", "unknown"},
		{"help", "image", "import", "extra"}, {"--help", "run"},
		{"ps", "--help", "extra"}, {"run", "--help", "--", "true"},
		{"exec", "--help", "worker"}, {"image", "--help", "ls"},
		{"image", "ls", "--json", "--help", "extra"},
	} {
		out, diagnostic, code := helpExecution(t, args)
		if code != 125 || out != "" || !strings.Contains(diagnostic, "Hint:") || strings.Contains(diagnostic, "Lima is required") {
			t.Errorf("invalid help %q: %d %q %q", args, code, out, diagnostic)
		}
	}
}

func TestHelpPreservesWorkloadAndOptionValues(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--rootfs", "/template", "--", "echo", "--help"},
		{"run", "--rootfs", "--help", "--", "true"},
		{"run", "--rootfs", "/template", "--env", "TOKEN=-h", "--", "echo", "-h"},
		{"exec", "worker", "--", "echo", "--help"},
		{"exec", "--env", "TOKEN=--help", "worker", "--", "true"},
	} {
		r, err := Parse(args)
		if err != nil || r.Action == "help" || r.HelpTopic != "" {
			t.Errorf("literal help intercepted: %q %+v %v", args, r, err)
		}
	}
	for _, args := range [][]string{
		{"run", "--rootfs", "-h", "--help"},
		{"run", "--rootfs", "/template", "--env", "TOKEN=--help", "-h"},
		{"exec", "--workdir", "--help", "--help"},
		{"logs", "--tail", "2", "--help"},
	} {
		r, err := Parse(args)
		if err != nil || r.Action != "help" || r.HelpTopic != args[0] {
			t.Errorf("help after option values lost: %q %+v %v", args, r, err)
		}
	}
}
