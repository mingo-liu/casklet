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

func TestShortIDImageAndContainerLifecycle(t *testing.T) {
	directory := hostDirectory(t)
	template := filepath.Join(directory, "template")
	success(t, "rootfs", template)
	if err := os.WriteFile(filepath.Join(template, "short-id-marker"), []byte(fmt.Sprint(time.Now().UnixNano())), 0644); err != nil {
		t.Fatal(err)
	}
	imageID := strings.TrimSpace(success(t, "image", "import", template))
	t.Cleanup(func() { command(t, "image", "rm", imageID) })
	shortImage := strings.TrimPrefix(imageID, "sha256:")[:12]
	if out := success(t, "run", "--image", shortImage, "--", "/bin/echo", "short-image"); out != "short-image\n" {
		t.Fatalf("short image run: %q", out)
	}
	id := detached(t, "--image", shortImage, "--", "/bin/sh", "-c", "echo short-ready; sleep 300")
	short := id[:12]
	if inspection := inspectHost(t, short); inspection.ID != id {
		t.Fatalf("inspection did not retain full ID: %+v", inspection)
	}
	success(t, "stats", "--json", "--interval", "10ms", short)
	if out := success(t, "exec", short, "--", "/bin/echo", "short-exec"); out != "short-exec\n" {
		t.Fatalf("short exec: %q", out)
	}
	if out := success(t, "logs", short); !strings.Contains(out, "short-ready") {
		t.Fatalf("short logs: %q", out)
	}
	for _, action := range []string{"restart", "stop", "start", "stop"} {
		if out := success(t, action, short); strings.TrimSpace(out) != id {
			t.Fatalf("%s did not return full ID: %q", action, out)
		}
	}
	if _, diagnostic, code := command(t, "wait", short); code == 125 || strings.Contains(diagnostic, "not found") {
		t.Fatalf("short wait failed lookup: %d %q", code, diagnostic)
	}
	if _, diagnostic, code := command(t, "image", "rm", shortImage); code != 125 || !strings.Contains(diagnostic, "referenced") {
		t.Fatalf("short ID bypassed retained image reference: %d %q", code, diagnostic)
	}
	success(t, "rm", short)
	success(t, "image", "rm", shortImage)
}
