//go:build darwin

package macos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCopyFilesThroughMacTransport(t *testing.T) {
	client(t)
	// The default Mac temporary directory is outside the product VM's shares.
	local := t.TempDir()
	source := filepath.Join(local, "source ' $ file")
	if err := os.WriteFile(source, []byte("exact\x00bytes\xff"), 0640); err != nil {
		t.Fatal(err)
	}
	id := detached(t, "--", "/bin/sleep", "300")
	success(t, "cp", source, id+":/")
	if got := success(t, "exec", id, "--", "/bin/cat", "/"+filepath.Base(source)); got != "exact\x00bytes\xff" {
		t.Fatalf("root destination %q", got)
	}
	protected := filepath.Join(local, "proc")
	if err := os.WriteFile(protected, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, code := command(t, "cp", protected, id+":/"); code != 125 {
		t.Fatal("overwrote a protected root entry")
	}
	success(t, "cp", source, id[:12]+":/copied")
	if got := success(t, "exec", id, "--", "/bin/cat", "/copied"); got != "exact\x00bytes\xff" {
		t.Fatalf("upload %q", got)
	}
	success(t, "cp", id+":/copied", filepath.Join(local, "download"))
	if got, err := os.ReadFile(filepath.Join(local, "download")); err != nil || string(got) != "exact\x00bytes\xff" {
		t.Fatalf("download %q %v", got, err)
	}
	success(t, "exec", id, "--", "/bin/sh", "-c", "mkdir -p /tree/nested; echo payload > /tree/nested/value; ln -s nested/value /tree/link")
	success(t, "cp", id+":/tree", filepath.Join(local, "renamed"))
	if link, err := os.Readlink(filepath.Join(local, "renamed", "link")); err != nil || link != "nested/value" {
		t.Fatalf("link %q %v", link, err)
	}
	success(t, "exec", id, "--", "/bin/mkdir", "/merge")
	success(t, "cp", filepath.Join(local, "renamed")+"/.", id+":/merge")
	if out := success(t, "exec", id, "--", "/bin/cat", "/merge/nested/value"); out != "payload\n" {
		t.Fatal(out)
	}
	success(t, "stop", id)
	success(t, "cp", source, id+":/stopped")
	success(t, "cp", id+":/stopped", filepath.Join(local, "stopped-copy"))
	success(t, "start", id)
	if out := success(t, "exec", id, "--", "/bin/cat", "/stopped"); out != "exact\x00bytes\xff" {
		t.Fatalf("retention %q", out)
	}
	for _, target := range []string{"/proc/self/status", "/dev/null", "/sys/kernel", "/"} {
		if _, _, code := command(t, "cp", id+":"+target, filepath.Join(local, "forbidden")); code != 125 {
			t.Fatalf("copied %s", target)
		}
	}
}

func TestCopyOverlayAndMountedStorage(t *testing.T) {
	client(t)
	directory := hostDirectory(t)
	source := filepath.Join(directory, "rootfs")
	success(t, "rootfs", source)
	image := strings.TrimSpace(success(t, "image", "import", source))
	t.Cleanup(func() { command(t, "image", "rm", image) })
	bind := filepath.Join(directory, "bind")
	if err := os.Mkdir(bind, 0755); err != nil {
		t.Fatal(err)
	}
	id := detached(t, "--image", image, "--mount", "type=bind,source="+bind+",target=/data", "--", "/bin/sleep", "300")
	local := filepath.Join(t.TempDir(), "payload")
	os.WriteFile(local, []byte("overlay-copy"), 0600)
	success(t, "cp", local, id+":/upper")
	success(t, "cp", local, id+":/data/value")
	if got, _ := os.ReadFile(filepath.Join(bind, "value")); string(got) != "overlay-copy" {
		t.Fatalf("live mount %q", got)
	}
	success(t, "stop", id)
	success(t, "cp", local, id+":/stopped-upper")
	downloaded := filepath.Join(t.TempDir(), "out")
	success(t, "cp", id+":/upper", downloaded)
	if got, _ := os.ReadFile(downloaded); string(got) != "overlay-copy" {
		t.Fatalf("stopped overlay %q", got)
	}
	if _, _, code := command(t, "cp", local, id+":/data/hidden"); code != 125 {
		t.Fatal("wrote hidden bind directory")
	}
	success(t, "start", id)
	if out := success(t, "exec", id, "--", "/bin/cat", "/stopped-upper"); out != "overlay-copy" {
		t.Fatalf("overlay restart %q", out)
	}
	readonly := detached(t, "--read-only", "--", "/bin/sleep", "300")
	if _, _, code := command(t, "cp", local, readonly+":/blocked"); code != 125 {
		t.Fatal("wrote read-only root")
	}
	success(t, "cp", local, readonly+":/tmp/allowed")
	success(t, "stop", readonly)
	if _, _, code := command(t, "cp", local, readonly+":/blocked"); code != 125 {
		t.Fatal("wrote stopped read-only root")
	}
}
