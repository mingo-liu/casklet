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
	"reflect"
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
	references := func() []string {
		t.Helper()
		var records []image.Record
		if err := json.Unmarshal([]byte(success(t, "image", "ls", "--json")), &records); err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.ID == id {
				return record.References
			}
		}
		t.Fatal("image is missing")
		return nil
	}
	beforeReferences := references()
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
	if afterReferences := references(); !reflect.DeepEqual(beforeReferences, afterReferences) {
		t.Fatalf("load changed cached registry mappings: %v => %v", beforeReferences, afterReferences)
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
			// Bound inherited stderr copies if a broken client leaves SSH alive.
			// The context assertion below still fails any missed cancellation.
			cmd.WaitDelay = 3 * time.Second
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if action == "load" {
				cmd.Stdin = reader
				cmd.Stdout = io.Discard
			} else {
				cmd.Stdout = writer
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if action == "save" {
				deadline, _ := ctx.Deadline()
				if err := reader.SetReadDeadline(deadline); err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
					t.Fatalf("save never started output: %v", err)
				}
			}
			time.Sleep(time.Second)
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			if ctx.Err() != nil {
				t.Fatalf("blocked %s did not cancel: %s", action, diagnostic.String())
			}
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 143 {
				t.Fatalf("%s cancellation: %v: %s", action, err, diagnostic.String())
			}
		})
	}
	// Deletion must succeed after canceled output, proving its guest image lease
	// no longer keeps the image busy.
	success(t, "image", "rm", id)
}
