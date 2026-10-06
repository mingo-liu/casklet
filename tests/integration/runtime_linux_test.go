//go:build linux

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var binary, template string
var sequence atomic.Uint64

func TestMain(m *testing.M) {
	if os.Getenv("MINI_DOCKER_INTEGRATION") != "1" {
		os.Exit(m.Run())
	}
	if err := prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "integration prerequisites:", err)
		_ = cleanupTemplate()
		os.Exit(1)
	}
	code := m.Run()
	if err := cleanupTemplate(); err != nil {
		fmt.Fprintln(os.Stderr, "integration rootfs cleanup:", err)
		code = 1
	}
	os.Exit(code)
}

func cleanupTemplate() error {
	if template == "" {
		return nil
	}
	return os.RemoveAll(template)
}

func prepare() error {
	if os.Geteuid() != 0 {
		return errors.New("root privileges are required")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return err
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("a running systemd system instance is required")
	}
	binary = os.Getenv("MINI_DOCKER_BINARY")
	rootfs := os.Getenv("MINI_DOCKER_ROOTFS")
	helper := os.Getenv("MINI_DOCKER_HELPER")
	for _, path := range []string{binary, rootfs, helper} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("binary, rootfs, and helper must be configured as absolute paths: %q", path)
		}
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	var err error
	template, err = os.MkdirTemp("", "mini-docker-integration-rootfs-")
	if err != nil {
		return err
	}
	if output, err := exec.Command("cp", "-a", rootfs+"/.", template).CombinedOutput(); err != nil {
		return fmt.Errorf("copy rootfs: %w: %s", err, output)
	}
	input, err := os.Open(helper)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(filepath.Join(template, "bin", "integration-helper"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0755)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(err, closeErr)
}

func require(t *testing.T) {
	t.Helper()
	if os.Getenv("MINI_DOCKER_INTEGRATION") != "1" {
		t.Skip("set MINI_DOCKER_INTEGRATION=1 through scripts/test-linux.sh for privileged Linux tests")
	}
}

type invocation struct {
	cmd            *exec.Cmd
	unit           string
	stdout, stderr *os.File
	cancel         context.CancelFunc
	waited         bool
}

func start(t *testing.T, input string, args ...string) *invocation {
	t.Helper()
	require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	unit := fmt.Sprintf("mini-docker-test-%d-%d.scope", os.Getpid(), sequence.Add(1))
	command := append([]string{"--scope", "--quiet", "--unit=" + unit, "--property=Delegate=cpu memory pids", "--", binary}, args...)
	stdout, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "systemd-run", command...)
	cmd.Env = append(os.Environ(), "MINI_DOCKER_HOST_SECRET=host-only-secret")
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	call := &invocation{cmd: cmd, unit: unit, stdout: stdout, stderr: stderr, cancel: cancel}
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = exec.CommandContext(stopCtx, "systemctl", "stop", unit).Run()
		if !call.waited && cmd.Process != nil {
			_ = cmd.Wait()
		}
		_ = stdout.Close()
		_ = stderr.Close()
	})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return call
}

func (call *invocation) wait(t *testing.T) (int, string, string) {
	t.Helper()
	call.waited = true
	err := call.cmd.Wait()
	call.cancel()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("wait: %v", err)
		}
		code = exit.ExitCode()
	}
	stdout, _ := os.ReadFile(call.stdout.Name())
	stderr, _ := os.ReadFile(call.stderr.Name())
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, _ := exec.Command("systemctl", "is-active", call.unit).Output()
		active := strings.TrimSpace(string(state))
		if active != "active" && active != "deactivating" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scope remains populated after exit: %s stdout=%q stderr=%q", call.unit, stdout, stderr)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return code, string(stdout), string(stderr)
}

func run(t *testing.T, options []string, command ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"run", "--rootfs", template}, options...)
	args = append(args, "--")
	args = append(args, command...)
	return start(t, "", args...).wait(t)
}

func success(t *testing.T, options []string, command ...string) string {
	t.Helper()
	code, out, stderr := run(t, options, command...)
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	return out
}

func TestDoctor(t *testing.T) {
	code, stdout, stderr := start(t, "", "doctor", "--rootfs", template).wait(t)
	if code != 0 {
		t.Fatalf("doctor exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}

func TestExecution(t *testing.T) {
	if out := success(t, nil, "/bin/echo", "hello"); out != "hello\n" {
		t.Fatalf("unexpected output %q", out)
	}
	code, _, stderr := run(t, nil, "/bin/sh", "-c", "exit 7")
	if code != 7 {
		t.Fatalf("exit=%d stderr=%q; expected 7", code, stderr)
	}
	code, out, stderr := start(t, "input survives\n", "run", "--rootfs", template, "--", "/bin/cat").wait(t)
	if code != 0 || out != "input survives\n" {
		t.Fatalf("stdin exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestIsolation(t *testing.T) {
	require(t)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.CreateTemp("/tmp", "mini-docker-host-marker-")
	if err != nil {
		t.Fatal(err)
	}
	marker.Close()
	t.Cleanup(func() { _ = os.Remove(marker.Name()) })
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	out := success(t, []string{"--hostname", "isolated-mini"}, "/bin/sh", "-c", `
set -eu
[ "$(hostname)" = isolated-mini ]
[ "$(cat /proc/1/comm)" != systemd ]
[ ! -e "$1" ]
for old in /.old-root-*; do [ ! -e "$old" ]; done
[ ! -e /sys/fs/cgroup ]
[ -c /dev/null ]
[ -c /dev/zero ]
printf isolated > /tmp/container-write
ip link | grep 'lo:'
[ "$(ip link | grep -c '^[0-9]')" = 1 ]
[ -z "$(ip route)" ]
[ -z "${HTTPS_PROXY:-}${AWS_ACCESS_KEY_ID:-}${MINI_DOCKER_HOST_SECRET:-}" ]
[ ! -e "/proc/$2" ]
echo isolation-ok`, "sh", marker.Name(), strconv.Itoa(os.Getpid()))
	if !strings.Contains(out, "isolation-ok") {
		t.Fatalf("unexpected output %q", out)
	}
	current, _ := os.Hostname()
	if current != hostname {
		t.Fatalf("host hostname changed: %q -> %q", hostname, current)
	}
	if _, err := os.Stat(filepath.Join(template, "tmp", "container-write")); !os.IsNotExist(err) {
		t.Fatalf("rootfs template changed: %v", err)
	}
	after, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil || string(mounts) != string(after) {
		t.Fatalf("host mount table changed: %v", err)
	}
}

func TestPrivileges(t *testing.T) {
	out := success(t, nil, "/bin/sh", "-c", `
set -eu
for field in CapEff CapPrm CapBnd CapAmb; do
  grep "^${field}:[[:space:]]*0000000000000000$" /proc/self/status
 done
grep '^NoNewPrivs:[[:space:]]*1$' /proc/self/status
mkdir /tmp/mount-check
if mount -t tmpfs tmpfs /tmp/mount-check; then exit 1; fi
echo privileges-ok`)
	if !strings.Contains(out, "privileges-ok") {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestResourceLimits(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		code, out, stderr := run(t, []string{"--memory", "128m", "--timeout", "15s"}, "/bin/integration-helper", "memory")
		if code == 0 || code == 124 || code == 125 || !strings.Contains(out, "allocating") {
			t.Fatalf("memory exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if !strings.Contains(strings.ToLower(stderr), "oom") {
			t.Fatalf("missing OOM diagnostic: %q", stderr)
		}
	})
	t.Run("pids", func(t *testing.T) {
		out := success(t, []string{"--pids-limit", "64", "--timeout", "15s"}, "/bin/integration-helper", "pids")
		if !strings.Contains(out, "process creation denied") {
			t.Fatalf("process limit was not enforced: %q", out)
		}
	})
}

func TestTimeout(t *testing.T) {
	code, out, stderr := run(t, []string{"--timeout", "200ms"}, "/bin/sh", "-c", "trap '' TERM; echo started; sleep 30")
	if code != 124 || !strings.Contains(out, "started") {
		t.Fatalf("timeout exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func (call *invocation) supervisorPID() int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	control, err := exec.CommandContext(ctx, "systemctl", "show", "--property=ControlGroup", "--value", call.unit).Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(control)), "/") {
		return 0
	}
	root := "/sys/fs/cgroup" + strings.TrimSpace(string(control))
	for _, path := range []string{filepath.Join(root, "manager", "cgroup.procs"), filepath.Join(root, "cgroup.procs")} {
		processes, _ := os.ReadFile(path)
		for _, value := range strings.Fields(string(processes)) {
			exe, _ := os.Readlink("/proc/" + value + "/exe")
			args, _ := os.ReadFile("/proc/" + value + "/cmdline")
			command := strings.Split(string(args), "\x00")
			if exe == binary && len(command) > 1 && command[1] == "run" {
				pid, _ := strconv.Atoi(value)
				return pid
			}
		}
	}
	return 0
}

func (call *invocation) supervisor(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := os.ReadFile(call.stdout.Name())
		if strings.Contains(string(out), "ready") {
			if pid := call.supervisorPID(); pid != 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	stdout, _ := os.ReadFile(call.stdout.Name())
	stderr, _ := os.ReadFile(call.stderr.Name())
	t.Fatalf("supervisor did not become ready: stdout=%q stderr=%q", stdout, stderr)
	return 0
}

func TestSignals(t *testing.T) {
	for _, signal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			call := start(t, "", "run", "--rootfs", template, "--", "/bin/sh", "-c", "trap 'echo interrupted; exit 0' INT TERM; echo ready; while :; do sleep 1; done")
			pid := call.supervisor(t)
			if err := syscall.Kill(pid, signal); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := call.wait(t)
			if code != 0 || !strings.Contains(out, "interrupted") {
				t.Fatalf("signal exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
		})
	}
}

func TestChildCleanup(t *testing.T) {
	out := success(t, []string{"--timeout", "10s"}, "/bin/sh", "-c", "sleep 30 & echo main-exited")
	if !strings.Contains(out, "main-exited") {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestStartupFailure(t *testing.T) {
	code, out, stderr := run(t, nil, "/bin/does-not-exist")
	if code != 125 {
		t.Fatalf("startup failure exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	success(t, nil, "/bin/echo", "recovered")
}

func TestRepetitionAndConcurrency(t *testing.T) {
	for i := 0; i < 20; i++ {
		if out := success(t, nil, "/bin/echo", "repeat"); out != "repeat\n" {
			t.Fatalf("run %d: %q", i, out)
		}
	}
	for i := 0; i < 2; i++ {
		t.Run(fmt.Sprintf("concurrent-%d", i), func(t *testing.T) {
			t.Parallel()
			if out := success(t, nil, "/bin/sh", "-c", "sleep 1; echo concurrent"); out != "concurrent\n" {
				t.Fatalf("unexpected output %q", out)
			}
		})
	}
}

func TestParentDeathRecovery(t *testing.T) {
	call := start(t, "", "run", "--rootfs", template, "--", "/bin/sh", "-c", "echo ready; sleep 30")
	pid := call.supervisor(t)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := call.wait(t)
	if code == 0 {
		t.Fatalf("killed supervisor returned success: %q", stderr)
	}
	if out := success(t, nil, "/bin/echo", "recovered"); out != "recovered\n" {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestRunDirectoriesClean(t *testing.T) {
	require(t)
	success(t, nil, "/bin/echo", "cleanup")
	entries, err := os.ReadDir("/var/lib/mini-docker/runs")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Errorf("leftover runtime directory: %s", entry.Name())
		}
	}
}

func TestExitCodeDuringDescendantCleanup(t *testing.T) {
	for _, expected := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit-%d", expected), func(t *testing.T) {
			command := fmt.Sprintf(`
set -eu
/bin/sh -c 'trap "" TERM; echo ready > /tmp/orphan-ready; while :; do sleep 30; done' &
while [ ! -e /tmp/orphan-ready ]; do sleep 0.01; done
echo main-exited
exit %d`, expected)
			started := time.Now()
			code, out, stderr := run(t, []string{"--timeout", "500ms"}, "/bin/sh", "-c", command)
			if code != expected || !strings.Contains(out, "main-exited") {
				t.Fatalf("completed command exit=%d stdout=%q stderr=%q; expected %d", code, out, stderr, expected)
			}
			if elapsed := time.Since(started); elapsed > 8*time.Second {
				t.Fatalf("descendant cleanup exceeded its bound: %v", elapsed)
			}
		})
	}
}

func TestSignalDuringPreparation(t *testing.T) {
	require(t)
	source := t.TempDir()
	if out, err := exec.Command("cp", "-a", template+"/.", source).CombinedOutput(); err != nil {
		t.Fatalf("copy template: %v: %s", err, out)
	}
	payload := filepath.Join(source, "startup-payload")
	if err := os.Mkdir(payload, 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		if err := os.WriteFile(filepath.Join(payload, fmt.Sprintf("payload-%05d", i)), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(payload, "zz-finished"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	previous := make(map[string]bool)
	entries, err := os.ReadDir("/var/lib/mini-docker/runs")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		previous[entry.Name()] = true
	}
	call := start(t, "", "run", "--rootfs", source, "--", "/bin/echo", "command-must-not-start")
	deadline := time.Now().Add(10 * time.Second)
	var interrupted string
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir("/var/lib/mini-docker/runs")
		for _, entry := range entries {
			if previous[entry.Name()] || !entry.IsDir() {
				continue
			}
			path := filepath.Join("/var/lib/mini-docker/runs", entry.Name())
			copied := filepath.Join(path, "rootfs", "startup-payload")
			if _, err := os.Stat(filepath.Join(copied, "payload-00000")); err != nil {
				continue
			}
			pid := call.supervisorPID()
			if _, err := os.Stat(filepath.Join(copied, "zz-finished")); pid != 0 && os.IsNotExist(err) {
				if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				interrupted = path
				break
			}
		}
		if interrupted != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if interrupted == "" {
		t.Fatal("did not observe the rootfs preparation phase before completion")
	}
	code, out, stderr := call.wait(t)
	if code != 128+int(syscall.SIGTERM) || strings.Contains(out, "command-must-not-start") {
		t.Fatalf("startup interruption exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if _, err := os.Stat(interrupted); !os.IsNotExist(err) {
		t.Fatalf("interrupted run directory remains: %v", err)
	}
}

func TestSignalWhileWaitingForStateLock(t *testing.T) {
	require(t)
	// Publish the production coordination lock through a completed runtime run.
	success(t, nil, "/bin/true")
	lockPath := "/var/lib/mini-docker/runs/.lock"
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
	call := start(t, "", "run", "--rootfs", template, "--", "/bin/echo", "command-must-not-start")
	deadline := time.Now().Add(10 * time.Second)
	pid := 0
	for time.Now().Before(deadline) {
		candidate := call.supervisorPID()
		if candidate != 0 {
			descriptors, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", candidate))
			for _, descriptor := range descriptors {
				target, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", candidate, descriptor.Name()))
				if target == lockPath {
					pid = candidate
					break
				}
			}
		}
		if pid != 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("supervisor did not reach the held state coordination lock")
	}
	started := time.Now()
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := call.wait(t)
	if code != 128+int(syscall.SIGTERM) || strings.Contains(out, "command-must-not-start") {
		t.Fatalf("state lock interruption exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("state lock interruption exceeded its bound: %v", elapsed)
	}
}
