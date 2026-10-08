//go:build darwin

package macos

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
	"golang.org/x/sys/unix"
)

func progressRegistry(t *testing.T) []string {
	t.Helper()
	success(t, "doctor")
	binary := filepath.Join(hostDirectory(t), "image-registry")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./testdata/image-registry")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build registry helper: %v %s", err, output)
	}
	cmd := exec.CommandContext(ctx, "limactl", "shell", "casklet-runtime", "sudo", "-n", "--", binary)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() {
		t.Fatalf("registry did not start: %v", scanner.Err())
	}
	var fixture struct {
		References []string
		PID        int
	}
	if err := json.Unmarshal(scanner.Bytes(), &fixture); err != nil || len(fixture.References) != 2 || fixture.PID <= 0 {
		t.Fatalf("registry handshake: %s %v", scanner.Bytes(), err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "limactl", "shell", "casklet-runtime", "sudo", "-n", "--", "kill", strconv.Itoa(fixture.PID)).Run()
	})
	return fixture.References
}

func ttyImagePull(t *testing.T, ref string) (string, string) {
	t.Helper()
	master, slave := openPTY(t)
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 100}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "image", "pull", ref)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, slave
	cmd.Env = append(os.Environ(), "TERM=xterm")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var stderr bytes.Buffer
	finished := false
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("TTY image pull: %v %s", err, stderr.String())
			}
			finished = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		poll := []unix.PollFd{{Fd: int32(master.Fd()), Events: unix.POLLIN}}
		if _, err := unix.Poll(poll, 100); err != nil && err != unix.EINTR {
			t.Fatal(err)
		}
		if poll[0].Revents&unix.POLLIN != 0 {
			var buffer [8192]byte
			n, err := unix.Read(int(master.Fd()), buffer[:])
			if err != nil && err != unix.EAGAIN {
				t.Fatal(err)
			}
			if n > 0 {
				stderr.Write(buffer[:n])
			}
		} else if finished {
			break
		}
	}
	return stdout.String(), stderr.String()
}

func TestImagePullProgressThroughVMAndHostTerminal(t *testing.T) {
	client(t)
	refs := progressRegistry(t)
	out, stderr, code := command(t, "image", "pull", refs[0])
	if code != 0 || image.ValidateID(strings.TrimSpace(out)) != nil || strings.Count(out, "\n") != 1 {
		t.Fatalf("plain image result: %d %q %q", code, out, stderr)
	}
	id := strings.TrimSpace(out)
	t.Cleanup(func() { command(t, "image", "rm", id) })
	for _, phase := range []string{"Pulling", "Downloading", "Extracting", "Pull complete", "Image ready:"} {
		if !strings.Contains(stderr, phase) {
			t.Fatalf("plain pull missing %s: %q", phase, stderr)
		}
	}
	if strings.ContainsAny(stderr, "\x1b\r") {
		t.Fatalf("non-terminal output contains controls: %q", stderr)
	}
	ttyOut, ttyStderr := ttyImagePull(t, refs[1])
	if ttyOut != id+"\n" || !strings.Contains(ttyStderr, "\x1b[1A") || !strings.Contains(ttyStderr, "Downloading [") || !strings.Contains(ttyStderr, "Extracting [") {
		t.Fatalf("host TTY progress or stream separation: %q %q", ttyOut, ttyStderr)
	}
	out, stderr, code = command(t, "image", "pull", "--progress=plain", refs[1])
	if code != 0 || out != id+"\n" || !strings.Contains(stderr, "Image is up to date") || strings.ContainsAny(stderr, "\x1b\r") {
		t.Fatalf("refresh progress: %d %q %q", code, out, stderr)
	}
	out, stderr, code = command(t, "run", "--image", refs[0])
	if code != 0 || out != "image-progress-ok\n" || !strings.Contains(stderr, "Using cached image") || strings.Contains(stderr, "Downloading") {
		t.Fatalf("cached run progress: %d %q %q", code, out, stderr)
	}
}
