package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectValidateRunsOfflineWithoutStateAndRedactsValues(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "must-not-exist")
	t.Setenv("CASKLET_MACHINE_STATE_DIR", state)
	t.Setenv("PATH", dir)
	if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte("TOKEN=validation-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "project.json")
	body := `{"name":"demo","volumes":["data"],"services":{"api":{"run":{"image":"example/app:1","env-file":["app.env"],"mount":["type=volume,source=data,target=/data"],"command":["secret-command"]},"depends-on":{"db":"started"}},"db":{"run":{"image":"example/db:1"}}}}`
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, jsonOutput := range []bool{false, true} {
		args := []string{"project", "validate", "--config", file}
		if jsonOutput {
			args = append(args, "--json")
		}
		out, diagnostic, code := helpExecution(t, args)
		if code != 0 || diagnostic != "" || !strings.Contains(out, "demo") {
			t.Fatalf("validate: %d %q %q", code, out, diagnostic)
		}
		for _, private := range []string{"validation-secret", "secret-command", "TOKEN", "example/app", "app.env"} {
			if strings.Contains(out+diagnostic, private) {
				t.Fatalf("validation disclosed %q: %q %q", private, out, diagnostic)
			}
		}
		if jsonOutput {
			var result struct {
				Valid    bool     `json:"valid"`
				Services []string `json:"services"`
			}
			if json.Unmarshal([]byte(out), &result) != nil || !result.Valid || strings.Join(result.Services, ",") != "db,api" {
				t.Fatalf("invalid summary: %s", out)
			}
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("validation accessed machine state: %v", err)
	}
}

func TestProjectValidateRejectsInvalidConfigAndFlagsWithScopedHelp(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, body := range []string{
		`{"name":"demo","services":{"api":{"run":{"image":"app:1","env":["BAD-KEY=validation-secret"]}}}}`,
		`{"name":"demo","services":{"api":{"run":{"image":"app:1"},"depends-on":{"missing":"started"}}}}`,
		`{"name":"demo","name":"other","services":{"api":{"run":{"image":"app:1"}}}}`,
		`{"name":"demo","services":{"api":{"run":{"image":"app:1","env-file":["missing.env"]}}}}`,
	} {
		file := projectConfigFile(t, body)
		out, diagnostic, code := helpExecution(t, []string{"project", "validate", "--config", file})
		if code != 125 || out != "" || !strings.Contains(diagnostic, "project validate --help") || strings.Contains(diagnostic, "validation-secret") {
			t.Fatalf("invalid validate: %d %q %q", code, out, diagnostic)
		}
	}
	for _, args := range [][]string{
		{"project", "validate"}, {"project", "validate", "--config", "x", "extra"},
		{"project", "validate", "--config", "x", "--dry-run"}, {"project", "validate", "--wait"},
	} {
		if _, err := Parse(args); err == nil || !strings.Contains(err.Error(), "project validate --help") {
			t.Fatalf("bad syntax accepted: %v %v", args, err)
		}
	}
}
