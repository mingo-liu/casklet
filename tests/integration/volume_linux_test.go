//go:build linux

package integration

import (
	"fmt"
	"golang.org/x/sys/unix"
	"strings"
	"testing"
	"time"
)

func TestNamedVolumePersistsAcrossContainerReplacement(t *testing.T) {
	require(t)
	name := fmt.Sprintf("volume-%d", time.Now().UnixNano())
	backgroundSuccess(t, "volume", "create", name)
	t.Cleanup(func() { backgroundCLI(t, "volume", "rm", name) })
	mount := "type=volume,source=" + name + ",target=/data"
	id := startBackground(t, backgroundName(t), []string{"--mount", mount}, "/bin/sh", "-c", "echo preserved > /data/message; echo volume-ready; sleep 300")
	lifecycleReady(t, id, "volume-ready")
	if code, _, _ := backgroundCLI(t, "volume", "rm", name); code != 125 {
		t.Fatal("removed active volume")
	}
	backgroundSuccess(t, "stop", id)
	if code, _, _ := backgroundCLI(t, "volume", "rm", name); code != 125 {
		t.Fatal("removed referenced stopped volume")
	}
	backgroundSuccess(t, "start", id)
	backgroundSuccess(t, "stop", id)
	backgroundSuccess(t, "rm", id)
	code, out, err := start(t, "", "run", "--rootfs", template, "--mount", mount+",readonly", "--", "/bin/sh", "-c", "cat /data/message; ! touch /data/refused").wait(t)
	if code != 0 || out != "preserved\n" || !strings.Contains(err, "Read-only") {
		t.Fatalf("volume reuse: %d %q %q", code, out, err)
	}
	backgroundSuccess(t, "volume", "rm", name)
}

func TestForegroundNamedVolumeLease(t *testing.T) {
	require(t)
	name := fmt.Sprintf("foreground-volume-%d", time.Now().UnixNano())
	backgroundSuccess(t, "volume", "create", name)
	t.Cleanup(func() { backgroundCLI(t, "volume", "rm", name) })
	call := start(t, "", "run", "--rootfs", template, "--mount", "type=volume,source="+name+",target=/data", "--", "/bin/sh", "-c", "echo ready; sleep 300")
	pid := call.supervisor(t)
	if code, _, _ := backgroundCLI(t, "volume", "rm", name); code != 125 {
		t.Fatal("removed foreground volume")
	}
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		t.Fatal(err)
	}
	call.wait(t)
	backgroundSuccess(t, "volume", "rm", name)
}
