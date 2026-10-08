//go:build darwin

package macos

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Resolve this invocation's private run through its workload cgroup. Other
// foreground sessions may be active in the product VM during these tests.
func foregroundRun(t *testing.T, membership string) (string, string) {
	t.Helper()
	relative := strings.TrimPrefix(strings.TrimSpace(membership), "0::")
	if !strings.HasPrefix(relative, "/") || !strings.Contains(relative, "/container-") {
		t.Fatalf("invalid workload cgroup: %q", membership)
	}
	cgroup := "/sys/fs/cgroup" + relative
	script := `for path in /var/lib/casklet/runs/run-*; do
  if [ -f "$path/state.json" ] && grep -Fq -- "\"cgroup\":\"$1\"" "$path/state.json"; then
    printf '%s\n' "$path"
  fi
done`
	output, err := guestRootCommand(t, "/bin/sh", "-c", script, "find-run", cgroup)
	path := strings.TrimSpace(output)
	if err != nil || !strings.HasPrefix(path, "/var/lib/casklet/runs/run-") || strings.Contains(path, "\n") {
		t.Fatalf("resolve foreground run: %q %v", output, err)
	}
	return path, cgroup
}

func waitForegroundCleanup(t *testing.T, path, cgroup string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := guestRootCommand(t, "/bin/sh", "-c", `test ! -e "$1" && test ! -e "$2"`, "check-cleanup", path, cgroup)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground resources remain: run=%s cgroup=%s: %v", path, cgroup, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	sum := sha256.Sum256([]byte(filepath.Base(path)))
	table := fmt.Sprintf("table ip casklet_%x", sum[:6])
	output, err := guestRootCommand(t, "nft", "list", "tables")
	if err != nil || strings.Contains(output, table) {
		t.Fatalf("foreground NAT cleanup: %q %v", output, err)
	}
}

func TestNonTTYHangupAndQuitCleanup(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal syscall.Signal
	}{{"HUP", syscall.SIGHUP}, {"QUIT", syscall.SIGQUIT}} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// Recovery is only a fallback for a failed test, after its assertions.
			t.Cleanup(func() { command(t, "run", "--", "/bin/true") })
			cmd := exec.CommandContext(ctx, client(t), "run", "--network", "bridge", "--", "/bin/sh", "-c", "trap 'echo signal-received; exit 9' HUP QUIT; cat /proc/self/cgroup; echo signal-ready; while :; do sleep 1; done")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			reader := bufio.NewReader(stdout)
			membership, err := reader.ReadString('\n')
			if err != nil {
				t.Fatalf("read cgroup: %v", err)
			}
			ready, err := reader.ReadString('\n')
			if err != nil || ready != "signal-ready\n" {
				t.Fatalf("readiness: %q %v", ready, err)
			}
			path, cgroup := foregroundRun(t, membership)
			if err := cmd.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			output, readErr := io.ReadAll(reader)
			err = cmd.Wait()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 9 || readErr != nil || !strings.Contains(string(output), "signal-received") {
				t.Fatalf("signal: %v read=%v output=%q stderr=%s", err, readErr, output, diagnostic.String())
			}
			waitForegroundCleanup(t, path, cgroup)
		})
	}
}
