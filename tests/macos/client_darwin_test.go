//go:build darwin

package macos

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func client(t *testing.T) string {
	t.Helper()
	if os.Getenv("CASKLET_MACOS_INTEGRATION") != "1" {
		t.Skip("set CASKLET_MACOS_INTEGRATION=1 or run make test-macos")
	}
	path := os.Getenv("CASKLET_MACOS_BINARY")
	if path == "" {
		path = "../../bin/casklet"
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

func command(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("client timed out: %q: %s", args, stderr.String())
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func success(t *testing.T, args ...string) string {
	t.Helper()
	out, diagnostic, code := command(t, args...)
	if code != 0 {
		t.Fatalf("casklet %q returned %d: %s %s", args, code, out, diagnostic)
	}
	return out
}

func hostDirectory(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(home, ".casklet-macos-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	return directory
}

func detached(t *testing.T, args ...string) string {
	t.Helper()
	id := strings.TrimSpace(success(t, append([]string{"run", "-d"}, args...)...))
	if len(id) != 32 {
		t.Fatalf("invalid ID: %q", id)
	}
	t.Cleanup(func() { command(t, "stop", "--timeout", "0s", id); command(t, "rm", id) })
	return id
}

func TestDefaultRootFSAndExactStreams(t *testing.T) {
	client(t)
	literal := "a'b $HOME $(printf unexpected)\nline"
	out, diagnostic, code := command(t, "run", "--env", "VALUE="+literal, "--", "/bin/sh", "-c", "printf '%s' \"$VALUE\"; printf 'stderr-only' >&2; exit 7")
	if code != 7 || out != literal || diagnostic != "stderr-only" {
		t.Fatalf("streams/exit: %d %q %q", code, out, diagnostic)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "run", "--", "/bin/cat")
	cmd.Stdin = strings.NewReader("stdin-exact\n")
	data, err := cmd.Output()
	if err != nil || string(data) != "stdin-exact\n" {
		t.Fatalf("stdin: %q %v", data, err)
	}
}

func TestMachineRepairsNonExecutableEngine(t *testing.T) {
	success(t, "doctor")
	guest := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "limactl", append([]string{"shell", "casklet-runtime", "sudo", "-n", "--"}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("guest command: %w: %s", err, output)
		}
		return nil
	}
	const engine = "/usr/local/bin/casklet"
	t.Cleanup(func() {
		if err := guest("chmod", "0755", engine); err != nil {
			t.Error(err)
		}
	})
	if err := guest("chmod", "0644", engine); err != nil {
		t.Fatal(err)
	}
	if out := success(t, "run", "--", "/bin/echo", "engine-repaired"); out != "engine-repaired\n" {
		t.Fatalf("repaired engine output: %q", out)
	}
}

func TestLifecycleRetainsContainerWrites(t *testing.T) {
	id := detached(t, "--", "/bin/sh", "-c", "echo retained >> /count; echo lifecycle-ready; sleep 60")
	waitForContents(t, id, "retained\n")
	if out := success(t, "logs", id); !strings.Contains(out, "lifecycle-ready") {
		t.Fatal(out)
	}
	success(t, "stats", "--json", id)
	success(t, "restart", "--timeout", "0s", id)
	waitForContents(t, id, "retained\nretained\n")
	success(t, "stop", "--timeout", "0s", id)
	success(t, "start", id)
	success(t, "inspect", id)
}

func waitForContents(t *testing.T, id, expected string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, diagnostic, code := command(t, "exec", id, "--", "/bin/cat", "/count")
		if code == 0 && out == expected {
			return
		}
		last = out + diagnostic
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("container did not publish %q: %s", expected, last)
}

func TestLiveBindAndLocalImage(t *testing.T) {
	directory := hostDirectory(t)
	file := filepath.Join(directory, "input")
	if err := os.WriteFile(file, []byte("host-value"), 0600); err != nil {
		t.Fatal(err)
	}
	mount := "type=bind,source=" + directory + ",target=/data"
	success(t, "run", "--mount", mount, "--", "/bin/sh", "-c", "cat /data/input > /data/output")
	data, err := os.ReadFile(filepath.Join(directory, "output"))
	if err != nil || string(data) != "host-value" {
		t.Fatalf("bind writeback: %q %v", data, err)
	}
	template := filepath.Join(directory, "template")
	success(t, "rootfs", template)
	// Add a unique file so the test owns its imported image identity.
	if err := os.WriteFile(filepath.Join(template, "unique"), []byte(directory), 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(success(t, "image", "import", template))
	t.Cleanup(func() { command(t, "image", "rm", id) })
	if out := success(t, "run", "--image", id, "--", "/bin/cat", "/unique"); out != directory {
		t.Fatal(out)
	}
	success(t, "image", "ls", "--json")
}

func TestRootlessUsesGuestIdentity(t *testing.T) {
	out := success(t, "run", "--rootless", "--", "/bin/sh", "-c", "id -u; cat /proc/self/gid_map")
	guest := exec.Command("limactl", "shell", "casklet-runtime", "id", "-g")
	data, err := guest.Output()
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out)
	if len(fields) != 4 || fields[0] != "0" || fields[2] != strings.TrimSpace(string(data)) {
		t.Fatalf("guest rootless mapping: %q (gid %s)", out, data)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func TestMachineStopStartRetainsContainers(t *testing.T) {
	id := detached(t, "--", "/bin/sh", "-c", "echo retained >> /count; sleep 300")
	waitForContents(t, id, "retained\n")
	var active []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(success(t, "ps", "--json")), &active); err != nil {
		t.Fatal(err)
	}
	for _, record := range active {
		if record.ID != id {
			t.Skip("preserving another active container; skipping runtime VM reboot")
		}
	}
	success(t, "machine", "stop")
	if out := success(t, "machine", "status"); !strings.Contains(out, "Stopped") {
		t.Fatalf("machine did not stop: %s", out)
	}
	success(t, "machine", "start")
	success(t, "start", id)
	waitForContents(t, id, "retained\nretained\n")
	if out := success(t, "run", "--rootless", "--", "/bin/id", "-u"); out != "0\n" {
		t.Fatalf("rootless after reboot: %s", out)
	}
}

func TestTTYWithoutInputDoesNotRequireHostTerminal(t *testing.T) {
	out, diagnostic, code := command(t, "run", "-t", "--", "/bin/sh", "-c", "test -t 1 && test -t 2; echo tty-without-input; cat")
	if code != 0 || !strings.Contains(out, "tty-without-input") {
		t.Fatalf("noninteractive TTY: %d %q %s", code, out, diagnostic)
	}
}

func TestPublishedTCPAndUDP(t *testing.T) {
	directory := hostDirectory(t)
	template := filepath.Join(directory, "template")
	success(t, "rootfs", template)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(template, "bin", "network-helper"), "../integration/testdata")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
	tcp, udp := freePort(t), freePort(t)
	id := detached(t, "--rootfs", template, "--network", "bridge", "-p", fmt.Sprintf("127.0.0.1:%d:8080", tcp), "-p", fmt.Sprintf("%d:7777/udp", udp), "--", "/bin/network-helper", "network-service")
	inspection := inspectHost(t, id)
	if len(inspection.Config.Publish) != 2 || inspection.Config.Publish[0].HostIP != "127.0.0.1" || inspection.Config.Publish[1].HostIP != "0.0.0.0" || inspection.Config.Publish[1].Protocol != "udp" {
		t.Fatalf("Mac published addresses: %+v", inspection.Config.Publish)
	}
	if len(inspection.GuestResources.Publish) != 2 || inspection.GuestResources.Publish[0].HostIP != "127.0.0.3" || inspection.GuestResources.Publish[1].HostIP != "127.0.0.2" {
		t.Fatalf("guest published addresses: %+v", inspection.GuestResources.Publish)
	}
	address := "127.0.0.1:" + strconv.Itoa(tcp)
	httpClient := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	deadline := time.Now().Add(20 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		response, err := httpClient.Get("http://" + address)
		if err == nil {
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if strings.Contains(string(data), "network-ok") {
				last = nil
				break
			}
			err = fmt.Errorf("unexpected response: %s", data)
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	if last != nil {
		t.Fatalf("TCP forwarding: %v", last)
	}
	connection, err := net.Dial("udp4", "127.0.0.1:"+strconv.Itoa(udp))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var buffer [64]byte
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		_ = connection.SetDeadline(time.Now().Add(500 * time.Millisecond))
		_, _ = connection.Write([]byte("udp-test"))
		n, err := connection.Read(buffer[:])
		if err == nil && string(buffer[:n]) == "echo:udp-test" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("UDP forwarding did not return its echo")
}

func TestSignalForwardingAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client(t), "run", "--", "/bin/sh", "-c", "trap 'echo signal-received; exit 9' TERM; echo signal-ready; while :; do sleep 1; done")
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
	buffer := make([]byte, 64)
	n, err := stdout.Read(buffer)
	if err != nil || !strings.Contains(string(buffer[:n]), "signal-ready") {
		t.Fatalf("readiness: %q %v", buffer[:n], err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(stdout)
	err = cmd.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 9 || !strings.Contains(string(data), "signal-received") {
		t.Fatalf("signal: %v %q %s", err, data, diagnostic.String())
	}
	// Listing remains usable after the remote foreground session ends.
	var records []json.RawMessage
	if err := json.Unmarshal([]byte(success(t, "ps", "--json")), &records); err != nil {
		t.Fatal(err)
	}
}

func TestAbruptClientLossCleansForegroundSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	marker := fmt.Sprintf("macos-client-loss-%d", time.Now().UnixNano())
	t.Cleanup(func() { command(t, "run", "--", "/bin/true") })
	cmd := exec.CommandContext(ctx, client(t), "run", "--network", "bridge", "--", "/bin/sh", "-c", "cat /proc/self/cgroup; echo "+marker+"; sleep 300")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil || strings.TrimSpace(ready) != marker {
		t.Fatalf("readiness: %q %v", ready, err)
	}
	path, cgroup := foregroundRun(t, membership)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probe := exec.CommandContext(ctx, "limactl", "shell", "casklet-runtime", "ps", "-eo", "args")
		data, err := probe.Output()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), marker) {
			waitForegroundCleanup(t, path, cgroup)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("foreground guest session survived abrupt client loss")
}
