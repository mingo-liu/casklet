//go:build linux

package integration

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestCopyRunningStoppedOverlayAndMounts(t *testing.T) {
	require(t)
	local := filepath.Join(t.TempDir(), "local ' $ file")
	if err := os.WriteFile(local, []byte("exact\x00bytes\xff"), 0640); err != nil {
		t.Fatal(err)
	}
	bind := t.TempDir()
	for _, overlay := range []bool{false, true} {
		options := []string{"--mount", "type=bind,source=" + bind + ",target=/data"}
		var id string
		if overlay {
			image := importImage(t, imageTemplate(t))
			args := []string{"run", "-d", "--name", backgroundName(t), "--image", image}
			args = append(args, options...)
			args = append(args, "--", "/bin/sleep", "300")
			id = backgroundID(t, backgroundSuccess(t, args...))
		} else {
			id = startBackground(t, backgroundName(t), options, "/bin/sleep", "300")
		}
		backgroundSuccess(t, "cp", local, id+":/")
		if got := backgroundSuccess(t, "exec", id, "--", "/bin/cat", "/"+filepath.Base(local)); got != "exact\x00bytes\xff" {
			t.Fatalf("root destination %q", got)
		}
		backgroundSuccess(t, "cp", local, id[:12]+":/value")
		backgroundSuccess(t, "cp", local, id+":/data/value")
		if got, _ := os.ReadFile(filepath.Join(bind, "value")); string(got) != "exact\x00bytes\xff" {
			t.Fatalf("mount %q", got)
		}
		out := filepath.Join(t.TempDir(), "out")
		backgroundSuccess(t, "cp", id+":/value", out)
		if got, _ := os.ReadFile(out); string(got) != "exact\x00bytes\xff" {
			t.Fatalf("download %q", got)
		}
		backgroundSuccess(t, "stop", id)
		backgroundSuccess(t, "cp", local, id+":/stopped")
		backgroundSuccess(t, "cp", id+":/value", out)
		for _, target := range []string{"/data/value", "/tmp/value", "/proc/self/status", "/sys/kernel", "/dev/null", "/"} {
			if code, _, _ := backgroundCLI(t, "cp", id+":"+target, out); code != 125 {
				t.Fatalf("copied stopped/protected %s", target)
			}
		}
		backgroundSuccess(t, "start", id)
		if got := backgroundSuccess(t, "exec", id, "--", "/bin/cat", "/stopped"); got != "exact\x00bytes\xff" {
			t.Fatalf("retained copy %q", got)
		}
		backgroundSuccess(t, "stop", id)
		backgroundSuccess(t, "rm", id)
	}
	readonly := startBackground(t, backgroundName(t), []string{"--read-only", "--mount", "type=bind,source=" + bind + ",target=/readonly,readonly"}, "/bin/sleep", "300")
	for _, target := range []string{"/blocked", "/readonly/value"} {
		if code, _, _ := backgroundCLI(t, "cp", local, readonly+":"+target); code != 125 {
			t.Fatalf("wrote readonly %s", target)
		}
	}
	backgroundSuccess(t, "cp", local, readonly+":/tmp/allowed")
	backgroundSuccess(t, "stop", readonly)
	if code, _, _ := backgroundCLI(t, "cp", local, readonly+":/blocked"); code != 125 {
		t.Fatal("wrote stopped read-only root")
	}
}

func TestCopyCancellationProtectsOldFileAndLifecycle(t *testing.T) {
	require(t)
	id := startBackground(t, backgroundName(t), nil, "/bin/sh", "-c", "echo original > /value; echo ready; sleep 300")
	lifecycleReady(t, id, "ready")
	backgroundSuccess(t, "stop", id)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	cmd := exec.CommandContext(ctx, binary, "__copy-in", id, "/value")
	cmd.Stdin = input
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	tw := tar.NewWriter(output)
	if err := tw.WriteHeader(&tar.Header{Name: "source", Typeflag: tar.TypeReg, Mode: 0600, Size: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("/var/lib/casklet/containers", id, "rootfs")
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, err := filepath.Glob(filepath.Join(root, ".casklet-cp-*"))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("copy did not reach staged write")
		}
		time.Sleep(10 * time.Millisecond)
	}
	store, err := container.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	lockCtx, lockCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	operation, err := store.AcquireOperation(lockCtx, id)
	lockCancel()
	if operation != nil {
		operation.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("copy did not fence lifecycle: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("canceled copy succeeded")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "value")); string(got) != "original\n" {
		t.Fatalf("replaced old file %q", got)
	}
	if stages, _ := filepath.Glob(filepath.Join(root, ".casklet-cp-*")); len(stages) != 0 {
		t.Fatalf("leaked staged files %v: %s", stages, diagnostic.String())
	}
	operation, err = store.AcquireOperation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	operation.Close()
	backgroundSuccess(t, "start", id)
}

func TestCopyWithoutContainerToolsAndArchiveEscape(t *testing.T) {
	require(t)
	tree := t.TempDir()
	os.Chmod(tree, 0755)
	helper, err := os.ReadFile(os.Getenv("CASKLET_HELPER"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "app"), helper, 0755); err != nil {
		t.Fatal(err)
	}
	id := backgroundID(t, backgroundSuccess(t, "run", "-d", "--name", backgroundName(t), "--rootfs", tree, "--", "/app", "copy-hold"))
	local := filepath.Join(t.TempDir(), "payload")
	os.WriteFile(local, []byte("no tools"), 0600)
	backgroundSuccess(t, "cp", local, id+":/value")
	out := filepath.Join(t.TempDir(), "out")
	backgroundSuccess(t, "cp", id+":/value", out)
	if got, _ := os.ReadFile(out); string(got) != "no tools" {
		t.Fatalf("tool-free %q", got)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0600})
	tw.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "__copy-in", id, "/safe")
	cmd.Stdin = &buf
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "invalid copy root") {
		t.Fatalf("archive escape %v %s", err, output)
	}
}
