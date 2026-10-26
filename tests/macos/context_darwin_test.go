//go:build darwin

package macos

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMachineContextCancellationCleansRemoteWorkload(t *testing.T) {
	client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	helper := filepath.Join(t.TempDir(), "context-client")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", helper, "./testdata/context-client").CombinedOutput(); err != nil {
		t.Fatalf("build context client: %v: %s", err, out)
	}
	cmd := exec.CommandContext(ctx, helper, "run", "--rootfs", "builtin:busybox", "--network", "bridge", "--", "/bin/sh", "-c", "trap 'echo context-cleanup; exit 9' TERM; cat /proc/self/cgroup; echo context-ready; while :; do sleep 1; done")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	reader := bufio.NewReader(output)
	membership, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read membership: %v", err)
	}
	if ready, err := reader.ReadString('\n'); err != nil || ready != "context-ready\n" {
		t.Fatalf("readiness: %q %v", ready, err)
	}
	path, cgroup := foregroundRun(t, membership)
	// This pipe cancels the helper's context; no process receives a host signal.
	if _, err := input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	err = cmd.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 125 || readErr != nil || !strings.Contains(string(data), "context-cleanup") || !strings.Contains(diagnostic.String(), "context canceled") {
		t.Fatalf("context cancellation: %v read=%v output=%s diagnostic=%s", err, readErr, data, diagnostic.String())
	}
	waitForegroundCleanup(t, path, cgroup)
}
