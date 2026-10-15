package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersionAliasesWorkWithoutRuntimeOrState(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("PATH", directory)
	t.Setenv("LIMA_HOME", filepath.Join(directory, "must-not-create"))
	t.Setenv("CASKLET_MACHINE_STATE_DIR", "relative-invalid-state")
	var plain, encoded string
	for _, command := range []string{"version", "--version"} {
		out, diagnostic, code := helpExecution(t, []string{command})
		if code != 0 || diagnostic != "" || !strings.HasPrefix(out, "casklet ") || !strings.Contains(out, "Platform: "+runtime.GOOS+"/"+runtime.GOARCH) {
			t.Fatalf("local version query: %d %q %q", code, out, diagnostic)
		}
		if plain != "" && out != plain {
			t.Fatal("version aliases differ")
		}
		plain = out
		out, diagnostic, code = helpExecution(t, []string{command, "--json"})
		var info versionInfo
		if code != 0 || diagnostic != "" || json.Unmarshal([]byte(out), &info) != nil {
			t.Fatalf("JSON version query: %d %q %q", code, out, diagnostic)
		}
		if info.Version == "" || info.Commit == "" || info.GoVersion != runtime.Version() || info.OS != runtime.GOOS || info.Arch != runtime.GOARCH {
			t.Fatalf("incomplete build metadata: %+v", info)
		}
		if runtime.GOOS == "darwin" {
			if info.Engine == nil && info.EngineError == "" {
				t.Fatal("missing engine is not diagnosed")
			}
			if info.Engine != nil && (info.Engine.OS != "linux" || info.Engine.Arch != runtime.GOARCH || len(info.Engine.SHA256) != 64) {
				t.Fatalf("invalid engine identity: %+v", info.Engine)
			}
		} else if info.Engine != nil || info.EngineError != "" {
			t.Fatal("internal Linux engine must not report another embedded engine")
		}
		if encoded != "" && encoded != out {
			t.Fatal("JSON version aliases differ")
		}
		encoded = out
	}
	if _, err := os.Stat(filepath.Join(directory, "must-not-create")); !os.IsNotExist(err) {
		t.Fatalf("local query initialized machine state: %v", err)
	}
}

func TestVersionRejectsArgumentsAndMatchesFocusedHelp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"version", "extra"}, {"--version", "run"}, {"version", "--unknown"}, {"--version", "--json", "extra"}} {
		out, diagnostic, code := helpExecution(t, args)
		if code != 125 || out != "" || !strings.Contains(diagnostic, "casklet version --help") {
			t.Fatalf("invalid version arguments: %q: %d %q %q", args, code, out, diagnostic)
		}
	}
	named, diagnostic, code := helpExecution(t, []string{"help", "version"})
	if code != 0 || diagnostic != "" {
		t.Fatalf("named version help: %d %s", code, diagnostic)
	}
	for _, args := range [][]string{{"version", "--help"}, {"--version", "--help"}} {
		out, diagnostic, code := helpExecution(t, args)
		if code != 0 || diagnostic != "" || out != named {
			t.Fatalf("version help differs: %d %q %q", code, out, diagnostic)
		}
	}
}

func TestVersionFlagRemainsLiteralInWorkloads(t *testing.T) {
	r, err := Parse([]string{"run", "--rootfs", "/template", "--", "/bin/echo", "--version"})
	if err != nil || r.Action != "run" || len(r.Config.Command) != 2 || r.Config.Command[1] != "--version" {
		t.Fatalf("workload version flag was intercepted: %+v %v", r, err)
	}
}
