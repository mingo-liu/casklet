//go:build darwin

package macos

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecCleanupFailureReturnsNonzeroAndPreservesCommandStatus(t *testing.T) {
	for _, status := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit-%d", status), func(t *testing.T) {
			client(t)
			id := detached(t, "--", "/bin/sleep", "300")
			original := inspectHost(t, id)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			script := fmt.Sprintf(`set -eu
cat /proc/self/cgroup
echo cleanup-ready
while [ ! -e /exec-cleanup-finish ]; do sleep 0.1; done
exit %d`, status)
			cmd := exec.CommandContext(ctx, client(t), "exec", id, "--", "/bin/sh", "-c", script)
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
			})
			reader := bufio.NewReader(output)
			membership, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			relative, ok := strings.CutPrefix(strings.TrimSpace(membership), "0::/")
			if !ok || strings.Contains(relative, "..") || !strings.Contains(relative, "casklet-"+id+".service/") || !strings.HasPrefix(filepath.Base(relative), "exec-") {
				t.Fatalf("unexpected exec cgroup: %q", membership)
			}
			if marker, err := reader.ReadString('\n'); err != nil || marker != "cleanup-ready\n" {
				t.Fatalf("exec readiness: %q %v", marker, err)
			}
			child := filepath.Join("/sys/fs/cgroup", relative, "preserve-unexpected-child")
			if _, err := guestRootCommand(t, "mkdir", "--", child); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := guestRootCommand(t, "/bin/sh", "-c", `if [ -d "$1" ]; then rmdir -- "$1"; fi`, "remove-test-child", child); err != nil {
					t.Error(err)
				}
			})
			success(t, "exec", id, "--", "/bin/touch", "/exec-cleanup-finish")
			remainder, readErr := io.ReadAll(reader)
			err = cmd.Wait()
			var exit *exec.ExitError
			want := status
			if want == 0 {
				want = 125
			}
			if ctx.Err() != nil || readErr != nil || !errors.As(err, &exit) || exit.ExitCode() != want || len(remainder) != 0 || !strings.Contains(diagnostic.String(), "preserve unexpected cgroup child") {
				t.Fatalf("cleanup result: err=%v want=%d read=%v ctx=%v stdout=%q stderr=%q", err, want, readErr, ctx.Err(), remainder, diagnostic.String())
			}
			if next := inspectHost(t, id); next.State != "running" || next.Generation != original.Generation {
				t.Fatalf("exec cleanup affected the main container: %+v", next)
			}
			if out := success(t, "exec", id, "--", "/bin/echo", "still-running"); out != "still-running\n" {
				t.Fatalf("next exec output: %q", out)
			}
		})
	}
}
