//go:build linux

package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/rootfs"
)

func TestImageCopyOnWriteIsolationWhiteoutsAndRestart(t *testing.T) {
	require(t)
	source := imageTemplate(t)
	writeTemplateFile(t, source, "bulk", strings.Repeat("unchanged bulk data\n", 256*1024), 0644)
	writeTemplateFile(t, source, "delete-me", "lower content", 0644)
	id := importImage(t, source)
	first := startImageContainer(t, backgroundName(t), id, "/bin/sleep", "300")
	second := startImageContainer(t, backgroundName(t), id, "/bin/sleep", "300")
	storage := rootfs.OverlayStoragePath(filepath.Join("/var/lib/casklet/containers", first, "rootfs"))
	if actual, ready, err := rootfs.ReadOverlayImageID(storage); err != nil || !ready || actual != id {
		t.Fatalf("retained overlay storage: %q, %v, %v", actual, ready, err)
	}
	if _, err := os.Lstat(filepath.Join(storage, "upper", "bulk")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unchanged bulk file copied: %v", err)
	}
	mounts := backgroundSuccess(t, "exec", first, "--", "/bin/cat", "/proc/mounts")
	if !strings.Contains(mounts, " / overlay ") || !strings.Contains(mounts, "nosuid") || !strings.Contains(mounts, "nodev") {
		t.Fatalf("root is not restricted OverlayFS: %s", mounts)
	}
	backgroundSuccess(t, "exec", first, "--", "/bin/sh", "-c", "rm /delete-me; mv /image-marker /renamed; echo persisted > /written")
	backgroundSuccess(t, "exec", second, "--", "/bin/sh", "-c", "[ -f /delete-me ] && [ -f /image-marker ] && [ ! -e /renamed ] && [ ! -e /written ]")
	backgroundSuccess(t, "stop", first)
	assertImageInUse(t, id)
	backgroundSuccess(t, "start", first)
	backgroundSuccess(t, "exec", first, "--", "/bin/sh", "-c", "[ ! -e /delete-me ] && [ ! -e /image-marker ] && [ -f /renamed ] && [ \"$(cat /written)\" = persisted ]")
	imageSuccess(t, id, nil, "/bin/sh", "-c", "[ -f /delete-me ] && [ -f /image-marker ] && [ ! -e /written ]")
	imageSuccess(t, id, []string{"--read-only"}, "/bin/sh", "-c", "if echo fail > /image-marker 2>/dev/null; then exit 91; fi; echo writable > /tmp/private")
}

func TestImageCopyOnWriteLowerLeaseSurvivesSupervisorLoss(t *testing.T) {
	require(t)
	id := importImage(t, imageTemplate(t))
	call := start(t, "", "run", "--image", id, "--stop-timeout", "2s", "--", "/bin/sh", "-c", "trap '' TERM; echo ready; while :; do sleep .1; done")
	pid := call.supervisor(t)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		files, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
		if errors.Is(err, os.ErrNotExist) || err == nil && len(files) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("supervisor did not close its descriptors after SIGKILL")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Foreground has no durable container reference. Init must keep its shared
	// lower-image lease while its namespace drains after the parent is gone.
	code, _, stderr := backgroundCLI(t, "image", "rm", id)
	if code != 125 || !strings.Contains(stderr, "referenced") {
		t.Fatalf("live lower deleted after parent loss: exit=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := call.wait(t); code == 0 {
		t.Fatalf("killed supervisor succeeded: %q", stderr)
	}
	imageSuccess(t, id, nil, "/bin/true") // also reclaims abandoned run storage
	backgroundSuccess(t, "image", "rm", id)
}

func TestImageLegacyCopiedRootRestartsWithoutLowerAndIgnoresMarker(t *testing.T) {
	require(t)
	id := importImage(t, imageTemplate(t))
	containerID := startImageContainer(t, backgroundName(t), id, "/bin/sleep", "300")
	backgroundSuccess(t, "stop", containerID)
	lower := filepath.Join("/var/lib/casklet/images", strings.TrimPrefix(id, "sha256:"), "rootfs")
	retained := filepath.Join("/var/lib/casklet/containers", containerID, "rootfs")
	if err := os.Mkdir(retained, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rootfs.CopyOwned(ctx, lower, retained); err != nil {
		t.Fatal(err)
	}
	writeTemplateFile(t, retained, ".casklet-overlay-v1.json", "ordinary workload data", 0644)
	writeTemplateFile(t, retained, "written", "legacy write", 0644)
	if err := os.Rename(lower, lower+".hidden"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Rename(lower+".hidden", lower); err != nil {
			t.Error(err)
		}
	}()
	backgroundSuccess(t, "start", containerID)
	if out := backgroundSuccess(t, "exec", containerID, "--", "/bin/cat", "/written"); out != "legacy write" {
		t.Fatalf("legacy write lost: %q", out)
	}
	backgroundSuccess(t, "stop", containerID)
}

func TestImageCopyOnWriteRejectsCorruptIdentityBeforeNewGeneration(t *testing.T) {
	require(t)
	id := importImage(t, imageTemplate(t))
	containerID := startImageContainer(t, backgroundName(t), id, "/bin/sleep", "300")
	backgroundSuccess(t, "stop", containerID)
	before := inspectBackground(t, containerID)
	storage := rootfs.OverlayStoragePath(filepath.Join("/var/lib/casklet/containers", containerID, "rootfs"))
	marker := filepath.Join(storage, ".casklet-overlay-v1.json")
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.WriteFile(marker, data, 0600); err != nil {
			t.Error(err)
		}
	}()
	wrong := "sha256:" + strings.Repeat("0", 64)
	if wrong == id {
		wrong = "sha256:" + strings.Repeat("1", 64)
	}
	if err := os.WriteFile(marker, []byte(strings.ReplaceAll(string(data), id, wrong)), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := backgroundCLI(t, "start", containerID); code != 125 || !strings.Contains(stderr, "overlay") {
		t.Fatalf("corrupt overlay start: exit=%d stderr=%q", code, stderr)
	}
	if after := inspectBackground(t, containerID); after.Generation != before.Generation || after.State != before.State {
		t.Fatalf("corrupt overlay advanced generation: before=%+v after=%+v", before, after)
	}
}
