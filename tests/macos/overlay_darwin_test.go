//go:build darwin

package macos

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestImageCopyOnWriteThroughMacClient(t *testing.T) {
	client(t)
	directory := hostDirectory(t)
	source := filepath.Join(directory, "template")
	success(t, "rootfs", source)
	for name, data := range map[string][]byte{
		"seed": []byte("original\n"), "removed": []byte("present\n"),
		"unique": []byte(directory), "bulk": make([]byte, 4<<20),
	} {
		if err := os.WriteFile(filepath.Join(source, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	image := strings.TrimSpace(success(t, "image", "import", source))
	t.Cleanup(func() { command(t, "image", "rm", image) })
	first := detached(t, "--image", image, "--health-cmd", "test -f /seed", "--health-interval", "100ms", "--", "/bin/sleep", "300")
	second := detached(t, "--image", image, "--", "/bin/sleep", "300")
	success(t, "wait", "--healthy", "--timeout", "10s", first)
	if out := success(t, "exec", first, "--", "/bin/sh", "-c", `awk '$5 == "/" {print $(NF-2)}' /proc/self/mountinfo`); out != "overlay\n" {
		t.Fatalf("container root is not copy-on-write: %q", out)
	}
	success(t, "exec", first, "--", "/bin/sh", "-c", "echo changed > /seed; rm /removed; echo created > /created")
	if out := success(t, "exec", second, "--", "/bin/sh", "-c", "cat /seed; cat /removed; test ! -e /created"); out != "original\npresent\n" {
		t.Fatalf("writes escaped the container: %q", out)
	}
	storage := "/var/lib/casklet/containers/" + first + "/rootfs.overlay"
	usage, err := guestRootCommand(t, "du", "-sk", "--", storage)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(usage)
	if len(fields) != 2 {
		t.Fatalf("invalid storage usage: %q", usage)
	}
	kib, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || kib >= 1024 {
		t.Fatalf("container storage duplicated the 4 MiB lower file: %q, %v", usage, err)
	}
	t.Logf("retained writable storage: %d KiB; unchanged lower fixture includes a 4 MiB file", kib)
	success(t, "stop", first)
	if _, _, code := command(t, "image", "rm", image); code != 125 {
		t.Fatal("removed the lower image of a retained container")
	}
	success(t, "start", first)
	success(t, "wait", "--healthy", "--timeout", "10s", first)
	if out := success(t, "exec", first, "--", "/bin/sh", "-c", "cat /seed /created; test ! -e /removed"); out != "changed\ncreated\n" {
		t.Fatalf("restart lost writable-layer data or whiteouts: %q", out)
	}
	if out := success(t, "run", "--image", image, "--read-only", "--", "/bin/sh", "-c", "cat /seed /removed; if echo invalid > /seed 2>/dev/null; then exit 1; fi"); out != "original\npresent\n" {
		t.Fatalf("read-only root or lower image changed: %q", out)
	}
}
