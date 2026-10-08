//go:build linux

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/volume"
	"golang.org/x/sys/unix"
)

func TestVolumeArchiveWithRetainedReferencesAndMountedTrees(t *testing.T) {
	require(t)
	name := fmt.Sprintf("archive-%d", time.Now().UnixNano())
	copyName := name + "-copy"
	backgroundSuccess(t, "volume", "create", name)
	t.Cleanup(func() { backgroundCLI(t, "volume", "rm", name); backgroundCLI(t, "volume", "rm", copyName) })
	id := startBackground(t, backgroundName(t), []string{"--mount", "type=volume,source=" + name + ",target=/data"}, "/bin/sh", "-c", "echo durable > /data/value; echo archive-ready; sleep 300")
	lifecycleReady(t, id, "archive-ready")
	if code, _, _ := backgroundCLI(t, "volume", "export", name); code != 125 {
		t.Fatal("exported mounted volume")
	}
	backgroundSuccess(t, "stop", id)
	if err := os.Chown(filepath.Join(config.VolumeRoot, name, "data", "value"), 1000, 1001); err != nil {
		t.Fatal(err)
	}
	archive := []byte(backgroundSuccess(t, "volume", "export", name))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "volume", "restore", copyName)
	cmd.Stdin = bytes.NewReader(archive)
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != copyName+"\n" {
		t.Fatalf("restore: %q %v", out, err)
	}
	code, outText, diagnostic := start(t, "", "run", "--rootfs", template, "--mount", "type=volume,source="+copyName+",target=/data,readonly", "--", "/bin/sh", "-c", "cat /data/value; stat -c '%u:%g' /data/value").wait(t)
	if code != 0 || outText != "durable\n1000:1001\n" {
		t.Fatalf("restored: %d %q %q", code, outText, diagnostic)
	}
	store, err := volume.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	mounted := filepath.Join(config.VolumeRoot, name, "data", "mounted")
	if err := os.Mkdir(mounted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(t.TempDir(), mounted, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	defer unix.Unmount(mounted, 0)
	var buf bytes.Buffer
	if err := store.Export(ctx, name, &buf); err == nil || !strings.Contains(err.Error(), "mount") {
		t.Fatalf("exported mounted tree: %v", err)
	}
}
