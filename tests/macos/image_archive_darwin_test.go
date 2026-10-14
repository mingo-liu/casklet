//go:build darwin

package macos

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
)

func TestOCIArchiveOfflineMacBinaryTransportAndRestart(t *testing.T) {
	client(t)
	fixture := progressRegistryDetails(t)
	id := strings.TrimSpace(success(t, "image", "pull", fixture.References[0]))
	t.Cleanup(func() { command(t, "image", "rm", id) })
	if _, err := guestRootCommand(t, "kill", strconv.Itoa(fixture.PID)); err != nil {
		t.Fatal(err)
	}
	archive := []byte(success(t, "image", "save", id))
	if _, err := tar.NewReader(bytes.NewReader(archive)).Next(); err != nil {
		t.Fatalf("nonbinary stdout: %v", err)
	}
	success(t, "image", "rm", id)
	out, diagnostic, code := registryInputCommand(t, string(archive), "image", "load")
	if code != 0 || out != id+"\n" {
		t.Fatalf("load: %d %q %q", code, out, diagnostic)
	}
	var records []image.Record
	if err := json.Unmarshal([]byte(success(t, "image", "ls", "--json")), &records); err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.ID == id && len(r.References) != 0 {
			t.Fatal("load recreated cached registry tags")
		}
	}
	if got := success(t, "run", "--image", id); got != "image-progress-ok\n" {
		t.Fatalf("offline default command: %q", got)
	}
	containerID := detached(t, "--image", id, "--entrypoint", "/bin/busybox", "--", "sh", "-c", "echo archive-ready; sleep 300")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(success(t, "logs", containerID), "archive-ready") {
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	success(t, "restart", containerID)
	if got := success(t, "exec", containerID, "--", "/bin/busybox", "echo", "restart-ok"); got != "restart-ok\n" {
		t.Fatalf("restart: %q", got)
	}
	success(t, "stop", containerID)
	success(t, "rm", containerID)
	for _, action := range []string{"load", "save"} {
		t.Run("cancel-"+action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			args := []string{"image", action}
			if action == "save" {
				args = append(args, id)
			}
			cmd := exec.CommandContext(ctx, client(t), args...)
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			cmd.Stderr = io.Discard
			if action == "load" {
				cmd.Stdin = reader
				cmd.Stdout = io.Discard
			} else {
				cmd.Stdout = writer
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if ctx.Err() != nil {
				t.Fatalf("blocked %s did not cancel", action)
			}
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 143 {
				t.Fatalf("%s cancellation: %v", action, err)
			}
		})
	}
}
