//go:build darwin

package macos

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMacSecretFilesProjectRestartAndCleanup(t *testing.T) {
	client(t)
	dir := hostDirectory(t)
	source := filepath.Join(dir, "token")
	os.WriteFile(source, []byte("private-first"), 0600)
	file := filepath.Join(dir, "project.json")
	name := fmt.Sprintf("secrets-%x", time.Now().UnixNano())
	doc := fmt.Sprintf(`{"name":%q,"networks":[],"services":{"app":{"run":{"network":"none","secret":["source=token,target=token"],"read-only":true,"user":"1000:1000","command":["/bin/sleep","300"]}}}}`, name)
	os.WriteFile(file, []byte(doc), 0600)
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	success(t, "project", "up", "--config", file, "--wait")
	ref := "cp-" + name + "-app"
	if got := success(t, "exec", ref, "--", "/bin/cat", "/run/secrets/token"); got != "private-first" {
		t.Fatalf("initial: %q", got)
	}
	if _, _, code := command(t, "exec", ref, "--", "/bin/sh", "-c", "echo change > /run/secrets/token"); code == 0 {
		t.Fatal("secret writable")
	}
	os.WriteFile(source, []byte("private-second"), 0600)
	if got := success(t, "exec", ref, "--", "/bin/cat", "/run/secrets/token"); got != "private-first" {
		t.Fatalf("running value changed: %q", got)
	}
	success(t, "project", "restart", name, "app")
	if got := success(t, "exec", ref, "--", "/bin/cat", "/run/secrets/token"); got != "private-second" {
		t.Fatalf("restart: %q", got)
	}
	if got := success(t, "inspect", ref); strings.Contains(got, "private-") {
		t.Fatal("inspect exposed content")
	}
	if _, _, code := command(t, "cp", ref+":/run/secrets/token", filepath.Join(dir, "copy")); code == 0 {
		t.Fatal("cp exposed secret")
	}
	success(t, "project", "stop", name, "app")
	os.Remove(source)
	if _, _, code := command(t, "project", "start", name, "app"); code == 0 {
		t.Fatal("missing source accepted")
	}
	success(t, "project", "down", name)
}
