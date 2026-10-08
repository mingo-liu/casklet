//go:build darwin

package macos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalConfigAndEnvFilesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	envfile := filepath.Join(dir, "app.env")
	filename := filepath.Join(dir, "run.json")
	if err := os.WriteFile(envfile, []byte("MODE=file\r\nLITERAL=$HOME # text\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(`{"detach":true,"env-file":["app.env"],"env":["MODE=config"],"command":["/bin/sh","-c","echo $MODE; sleep 300"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(success(t, "run", "--config", filename, "--env", "MODE=cli"))
	t.Cleanup(func() { command(t, "stop", id); command(t, "rm", id) })
	if out := success(t, "exec", "--env-file", envfile, "--env", "MODE=exec", id, "--", "/bin/sh", "-c", "printf '%s\\n' \"$MODE\" \"$LITERAL\""); out != "exec\n$HOME # text\n" {
		t.Fatal(out)
	}
	os.Remove(envfile)
	os.Remove(filename)
	success(t, "restart", id)
	if out := success(t, "exec", id, "--", "/bin/sh", "-c", "echo $MODE"); out != "cli\n" {
		t.Fatalf("saved environment: %q", out)
	}
}
