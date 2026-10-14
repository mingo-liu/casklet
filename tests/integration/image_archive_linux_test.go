//go:build linux

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOCIArchiveOfflineBinaryTransportAndRetainedRestart(t *testing.T) {
	require(t)
	ref, closeRegistry := ociRegistryFixture(t)
	id := strings.TrimSpace(backgroundSuccess(t, "image", "pull", ref))
	t.Cleanup(func() { backgroundCLI(t, "image", "rm", id) })
	closeRegistry()
	archive := []byte(backgroundSuccess(t, "image", "save", id))
	backgroundSuccess(t, "image", "rm", id)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "image", "load")
	cmd.Stdin = bytes.NewReader(archive)
	var out, diagnostic bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostic
	if err := cmd.Run(); err != nil || out.String() != id+"\n" {
		t.Fatalf("load: %q %q %v", out.String(), diagnostic.String(), err)
	}
	if got := imageSuccess(t, id, nil); got != "image:default:/new-work\n" {
		t.Fatalf("offline defaults: %q", got)
	}
	name := fmt.Sprintf("archive-%d", time.Now().UnixNano())
	containerID := startImageContainer(t, name, id, `printf '%s:%s\n' "$IMAGE_VALUE" "$PWD"; echo ready; sleep 300`, "entry")
	lifecycleReady(t, containerID, "ready")
	backgroundSuccess(t, "restart", containerID)
	if got := backgroundSuccess(t, "exec", containerID, "--", "/usr/bin/toolbox", "id", "-u"); got != "123\n" {
		t.Fatalf("saved user: %q", got)
	}
	backgroundSuccess(t, "stop", containerID)
	backgroundSuccess(t, "rm", containerID)
}
func TestOCIArchiveCancellationUnblocksInputAndOutput(t *testing.T) {
	require(t)
	ref, closeRegistry := ociRegistryFixture(t)
	id := strings.TrimSpace(backgroundSuccess(t, "image", "pull", ref))
	t.Cleanup(func() { backgroundCLI(t, "image", "rm", id) })
	closeRegistry()
	for _, action := range []string{"load", "save"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := []string{"image", action}
			if action == "save" {
				args = append(args, id)
			}
			cmd := exec.CommandContext(ctx, binary, args...)
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
			// The real static toolbox makes save exceed pipe capacity; neither endpoint
			// is consumed, so cancellation must close the blocked guest descriptor.
			time.Sleep(500 * time.Millisecond)
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
