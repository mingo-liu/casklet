//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type execInvocation struct {
	cmd            *exec.Cmd
	stdout, stderr *os.File
	done           chan error
	cancel         context.CancelFunc
	waited         bool
}

func startExec(t *testing.T, input string, options []string, reference string, command ...string) *execInvocation {
	t.Helper()
	require(t)
	args := append([]string{"exec"}, options...)
	args = append(args, reference, "--")
	args = append(args, command...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	stdout, err := os.CreateTemp(t.TempDir(), "exec-stdout-")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "exec-stderr-")
	if err != nil {
		cancel()
		stdout.Close()
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(os.Environ(), "MINI_DOCKER_HOST_SECRET=exec-host-only-secret")
	call := &execInvocation{cmd: cmd, stdout: stdout, stderr: stderr, done: make(chan error, 1), cancel: cancel}
	t.Cleanup(func() {
		cancel()
		if !call.waited && cmd.Process != nil {
			select {
			case <-call.done:
			case <-time.After(5 * time.Second):
				t.Error("exec client did not terminate during cleanup")
			}
		}
		stdout.Close()
		stderr.Close()
	})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { call.done <- cmd.Wait() }()
	return call
}

func (call *execInvocation) wait(t *testing.T) (int, string, string) {
	t.Helper()
	var err error
	select {
	case err = <-call.done:
		call.waited = true
	case <-time.After(12 * time.Second):
		out, _ := os.ReadFile(call.stdout.Name())
		stderr, _ := os.ReadFile(call.stderr.Name())
		t.Fatalf("exec did not terminate within its cleanup deadline: stdout=%q stderr=%q", out, stderr)
	}
	call.cancel()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("wait for exec: %v", err)
		}
		code = exit.ExitCode()
	}
	out, outErr := os.ReadFile(call.stdout.Name())
	stderr, errErr := os.ReadFile(call.stderr.Name())
	if outErr != nil || errErr != nil {
		t.Fatalf("read exec output: %v %v", outErr, errErr)
	}
	return code, string(out), string(stderr)
}

func (call *execInvocation) ready(t *testing.T, marker string) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		out, err := os.ReadFile(call.stdout.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), marker) {
			return string(out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, _ := os.ReadFile(call.stdout.Name())
	stderr, _ := os.ReadFile(call.stderr.Name())
	t.Fatalf("exec did not become ready (%s): stdout=%q stderr=%q", marker, out, stderr)
	return ""
}

func execSuccess(t *testing.T, options []string, reference string, command ...string) string {
	t.Helper()
	code, out, stderr := startExec(t, "", options, reference, command...).wait(t)
	if code != 0 {
		t.Fatalf("exec %q exit=%d stdout=%q stderr=%q", command, code, out, stderr)
	}
	return out
}

func execContainer(t *testing.T, options []string) (string, string) {
	t.Helper()
	name := backgroundName(t)
	id := startBackground(t, name, options, "/bin/sh", "-c", "echo main-ready; sleep 60")
	waitBackground(t, id, "running")
	return name, id
}

func execCgroup(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if relative, ok := strings.CutPrefix(line, "0::/"); ok {
			if relative == "" || strings.Contains(relative, "..") {
				t.Fatalf("unsafe exec cgroup path %q", relative)
			}
			return filepath.Join("/sys/fs/cgroup", relative)
		}
	}
	t.Fatalf("exec did not publish a cgroups v2 path: %q", output)
	return ""
}

func TestExecCleanupFailureReturnsNonzeroAndPreservesCommandStatus(t *testing.T) {
	for _, status := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit-%d", status), func(t *testing.T) {
			name, id := execContainer(t, nil)
			original := waitBackground(t, id, "running")
			call := startExec(t, "", nil, name, "/bin/sh", "-c", fmt.Sprintf(`
set -eu
cat /proc/self/cgroup
echo cleanup-ready
while [ ! -e /exec-cleanup-finish ]; do sleep 0.1; done
exit %d`, status))
			group := execCgroup(t, call.ready(t, "cleanup-ready\n"))
			if filepath.Dir(group) != original.Cgroup || !strings.HasPrefix(filepath.Base(group), "exec-") {
				t.Fatalf("unexpected exec cgroup: %q parent=%q", group, original.Cgroup)
			}
			// Preserve an unrecognized child to inject a real removal failure.
			child := filepath.Join(group, "preserve-unexpected-child")
			if err := os.Mkdir(child, 0755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Remove(child); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Error(err)
				}
			})
			execSuccess(t, nil, id, "/bin/touch", "/exec-cleanup-finish")
			code, out, diagnostic := call.wait(t)
			want := status
			if want == 0 {
				want = 125
			}
			if code != want || !strings.Contains(out, "cleanup-ready\n") || !strings.Contains(diagnostic, "preserve unexpected cgroup child") {
				t.Fatalf("cleanup result: exit=%d want=%d stdout=%q stderr=%q", code, want, out, diagnostic)
			}
			if next := waitBackground(t, id, "running"); next.Generation != original.Generation {
				t.Fatalf("exec cleanup changed the container generation: %+v", next)
			}
			if out := execSuccess(t, nil, id, "/bin/echo", "still-running"); out != "still-running\n" {
				t.Fatal("exec cleanup affected the main container")
			}
		})
	}
}

func TestExecSharedNamespacesAndFilesystem(t *testing.T) {
	name, id := execContainer(t, []string{"--hostname", "exec-host"})
	out := execSuccess(t, nil, name, "/bin/sh", "-c", `
set -eu
[ "$$" != 1 ]
# Inspect the shell's descriptors without opening a listing subprocess. The
# directory descriptor used for glob expansion has already closed here.
[ ! -L /proc/$$/fd/11 ]
for descriptor in /proc/$$/fd/*; do
  case "${descriptor##*/}" in 0|1|2) continue ;; esac
  if [ -L "$descriptor" ]; then
    echo "unexpected inherited descriptor: $descriptor" >&2
    readlink "$descriptor" >&2
    exit 1
  fi
done
[ "$(hostname)" = exec-host ]
for namespace in pid mnt uts ipc net; do
  [ "$(readlink /proc/self/ns/$namespace)" = "$(readlink /proc/1/ns/$namespace)" ]
done
printf shared-filesystem > /tmp/exec-shared
echo namespaces-ok`)
	if out != "namespaces-ok\n" {
		t.Fatalf("namespace check returned %q", out)
	}
	if out := execSuccess(t, nil, id, "/bin/cat", "/tmp/exec-shared"); out != "shared-filesystem" {
		t.Fatalf("exec did not share the running filesystem: %q", out)
	}
	if _, err := os.Stat(filepath.Join(template, "tmp", "exec-shared")); !os.IsNotExist(err) {
		t.Fatalf("exec modified the original template: %v", err)
	}
	waitBackground(t, id, "running")
}

func TestExecSurvivesLauncherRemoval(t *testing.T) {
	require(t)
	launcher := filepath.Join(t.TempDir(), "mini-docker")
	source, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := os.OpenFile(launcher, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(destination, source)
	if err := errors.Join(copyErr, destination.Close()); err != nil {
		t.Fatal(err)
	}
	name := backgroundName(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, launcher, detachedArguments(name, nil, "/bin/sleep", "60")...).CombinedOutput()
	if err != nil {
		t.Fatalf("start copied launcher: %v: %s", err, output)
	}
	id := backgroundID(t, string(output))
	waitBackground(t, id, "running")
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if out := execSuccess(t, nil, name, "/bin/echo", "pinned-launcher"); out != "pinned-launcher\n" {
		t.Fatalf("exec lost its pinned launcher: %q", out)
	}
	waitBackground(t, id, "running")
}

func TestExecEnvironmentWorkdirAndLookup(t *testing.T) {
	source := executionTemplate(t)
	writeTemplateFile(t, source, "workspace/inherited", "#!/bin/sh\nprintf 'inherited:%s:%s\\n' \"$PWD\" \"$VALUE\"\n", 0755)
	writeTemplateFile(t, source, "overrides/selected", "#!/bin/sh\nprintf 'override:%s:%s\\n' \"$PWD\" \"$VALUE\"\n", 0755)
	name, id := execContainer(t, []string{"--rootfs", source, "--workdir", "/workspace", "--env", "PATH=/workspace:/bin", "--env", "VALUE=base", "--env", "EMPTY="})
	if out := execSuccess(t, nil, name, "inherited"); out != "inherited:/workspace:base\n" {
		t.Fatalf("exec did not inherit lookup/workdir/environment: %q", out)
	}
	if out := execSuccess(t, []string{"--env", "PATH=/overrides:/bin", "--env", "VALUE=first", "--env", "VALUE=last", "--workdir", "/overrides"}, id, "selected"); out != "override:/overrides:last\n" {
		t.Fatalf("exec overrides were not applied: %q", out)
	}
	if out := execSuccess(t, nil, id, "/bin/sh", "-c", `[ "$VALUE" = base ] && [ "$PWD" = /workspace ] && [ "${EMPTY+x}" = x ] && [ -z "$EMPTY" ] && [ -z "${MINI_DOCKER_HOST_SECRET+x}" ] && echo environment-ok`); out != "environment-ok\n" {
		t.Fatalf("exec leaked client variables or changed saved defaults: %q", out)
	}
	for _, test := range []struct {
		options []string
		command string
	}{
		{[]string{"--workdir", "/missing"}, "/bin/echo"},
		{nil, "outside-configured-path"},
	} {
		code, out, stderr := startExec(t, "", test.options, name, test.command, "must-not-run").wait(t)
		if code != 125 || out != "" || stderr == "" {
			t.Fatalf("exec startup failure exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	}
	waitBackground(t, id, "running")
}

func TestExecInheritsIdentityAndRestrictions(t *testing.T) {
	source := executionTemplate(t)
	writeTemplateFile(t, source, "root-private", "root-private", 0600)
	writeTemplateFile(t, source, "public/existing", "unchanged", 0666)
	if err := os.Chmod(filepath.Join(source, "public"), 0777); err != nil {
		t.Fatal(err)
	}
	name, id := execContainer(t, []string{"--rootfs", source, "--user", "1000:1234", "--read-only"})
	if out := execSuccess(t, nil, name, "/bin/sh", "-c", checkIdentity, "sh", "1000", "1234"); out != "identity-ok\n" {
		t.Fatalf("exec identity/capability mismatch: %q", out)
	}
	out := execSuccess(t, nil, id, "/bin/sh", "-c", `
set -eu
if (printf changed > /public/existing) 2>/tmp/exec-write-error; then exit 1; fi
grep -q 'Read-only file system' /tmp/exec-write-error
[ "$(cat /public/existing)" = unchanged ]
printf scratch > /tmp/exec-writable
[ "$(cat /tmp/exec-writable)" = scratch ]
echo restrictions-ok`)
	if out != "restrictions-ok\n" {
		t.Fatalf("exec restriction check returned %q", out)
	}
	if code, _, _ := backgroundCLI(t, "exec", "--user", "0", name, "--", "/bin/id"); code != 125 {
		t.Fatal("exec accepted a user override")
	}
	waitBackground(t, id, "running")
}

func TestExecStreamsInputAndActualExit(t *testing.T) {
	name, id := execContainer(t, nil)
	code, out, stderr := startExec(t, "discarded-input\n", nil, name, "/bin/cat").wait(t)
	if code != 0 || out != "" || stderr != "" {
		t.Fatalf("default exec stdin is not EOF: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = startExec(t, "forwarded-input\n", []string{"-i"}, id, "/bin/sh", "-c", "cat; echo exec-error >&2; exit 7").wait(t)
	if code != 7 || out != "forwarded-input\n" || stderr != "exec-error\n" {
		t.Fatalf("exec streams/exit mismatch: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if logs := backgroundSuccess(t, "logs", name); strings.Contains(logs, "forwarded-input") || strings.Contains(logs, "exec-error") {
		t.Fatalf("exec output leaked into background logs: %q", logs)
	}
	code, _, _ = startExec(t, "", nil, id, "/bin/sh", "-c", "kill -TERM $$").wait(t)
	if code != 143 {
		t.Fatalf("command signal exit=%d; want 143", code)
	}
	waitBackground(t, id, "running")
}

func TestExecSessionCgroupAndDescendantCleanup(t *testing.T) {
	name, id := execContainer(t, []string{"--memory", "96m", "--pids-limit", "64", "--cpus", ".5"})
	record := waitBackground(t, id, "running")
	data, err := os.ReadFile(filepath.Join(record.RunPath, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Cgroup string `json:"cgroup"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	call := startExec(t, "", nil, name, "/bin/sh", "-c", `
set -eu
setsid /bin/sh -c 'trap "" TERM; echo ready > /tmp/exec-orphan-ready; sleep 30' &
while [ ! -e /tmp/exec-orphan-ready ]; do sleep 0.01; done
cat /proc/self/cgroup
echo exec-ready
exit 9`)
	code, out, stderr := call.wait(t)
	if code != 9 || !strings.Contains(out, "exec-ready\n") {
		t.Fatalf("exec descendant cleanup exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	group := execCgroup(t, out)
	if metadata.Cgroup == "" || group == metadata.Cgroup || !strings.HasPrefix(group, metadata.Cgroup+string(os.PathSeparator)) {
		t.Fatalf("exec did not use a child of the workload cgroup: exec=%s workload=%s", group, metadata.Cgroup)
	}
	assertCgroupRemoved(t, group)
	if out := execSuccess(t, nil, id, "/bin/echo", "still-running"); out != "still-running\n" {
		t.Fatal("exec cleanup stopped the container")
	}
}

func TestExecCPUQuota(t *testing.T) {
	name, id := execContainer(t, []string{"--cpus", ".25"})
	record := waitBackground(t, id, "running")
	data, err := os.ReadFile(filepath.Join(record.RunPath, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Cgroup string `json:"cgroup"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Cgroup == "" {
		t.Fatalf("read workload cgroup: metadata=%+v error=%v", metadata, err)
	}
	quota, err := os.ReadFile(filepath.Join(metadata.Cgroup, "cpu.max"))
	if err != nil || strings.TrimSpace(string(quota)) != "25000 100000" {
		t.Fatalf("ancestor CPU quota = %q error=%v; want 25000 100000", quota, err)
	}
	throttledPeriods := func() uint64 {
		stats, err := os.ReadFile(filepath.Join(metadata.Cgroup, "cpu.stat"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(stats), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "nr_throttled" {
				count, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				return count
			}
		}
		t.Fatalf("missing CPU throttling counter: %q", stats)
		return 0
	}
	before := throttledPeriods()
	call := startExec(t, "", []string{"--timeout", "10s"}, name, "/bin/sh", "-c", "cat /proc/self/cgroup; exec /bin/integration-helper cpu")
	code, out, stderr := call.wait(t)
	if code != 0 {
		t.Fatalf("exec CPU workload exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	var elapsedUS, cpuUS int64
	for _, field := range strings.Fields(out) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || (key != "elapsed_us" && key != "cpu_us") {
			continue
		}
		measurement, err := strconv.ParseInt(value, 10, 64)
		if err != nil || measurement <= 0 {
			t.Fatalf("invalid exec CPU measurement: %q", field)
		}
		if key == "elapsed_us" {
			elapsedUS = measurement
		} else {
			cpuUS = measurement
		}
	}
	if elapsedUS < 3000000 || cpuUS <= 0 {
		t.Fatalf("missing or too short exec CPU measurement: %q", out)
	}
	if ratio := float64(cpuUS) / float64(elapsedUS); ratio < .10 || ratio > .50 {
		t.Fatalf("exec did not inherit the quarter-CPU quota: usage ratio=%.3f stdout=%q", ratio, out)
	}
	after := throttledPeriods()
	if after <= before {
		t.Fatalf("exec CPU workload did not increase ancestor throttling: before=%d after=%d", before, after)
	}
	assertCgroupRemoved(t, execCgroup(t, out))
	waitBackground(t, id, "running")
	t.Logf("exec quarter-CPU quota: elapsed_us=%d cpu_us=%d throttled_periods=%d", elapsedUS, cpuUS, after-before)
}

func TestExecSignalAndTimeoutPreserveContainer(t *testing.T) {
	name, id := execContainer(t, nil)
	for _, test := range []struct {
		name   string
		signal syscall.Signal
		trap   string
		want   int
	}{
		{"interrupt", syscall.SIGINT, "trap 'exit 42' INT", 42},
		{"termination", syscall.SIGTERM, "trap 'exit 43' TERM", 43},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := startExec(t, "", nil, name, "/bin/sh", "-c", test.trap+"; cat /proc/self/cgroup; echo signal-ready; while :; do sleep .1; done")
			group := execCgroup(t, call.ready(t, "signal-ready\n"))
			if err := call.cmd.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			code, out, stderr := call.wait(t)
			if code != test.want {
				t.Fatalf("exec signal did not preserve trap exit: exit=%d stdout=%q stderr=%q", code, out, stderr)
			}
			assertCgroupRemoved(t, group)
			waitBackground(t, id, "running")
		})
	}
	call := startExec(t, "", []string{"--timeout", "200ms"}, id, "/bin/sh", "-c", "cat /proc/self/cgroup; echo timeout-ready; sleep 30")
	code, out, stderr := call.wait(t)
	if code != 124 || !strings.Contains(out, "timeout-ready\n") {
		t.Fatalf("exec timeout exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertCgroupRemoved(t, execCgroup(t, out))
	waitBackground(t, id, "running")
}

func TestExecClientLossCleansOnlyItsSession(t *testing.T) {
	name, id := execContainer(t, nil)
	call := startExec(t, "", nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo lost-client-ready; sleep 30")
	group := execCgroup(t, call.ready(t, "lost-client-ready\n"))
	if err := call.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	call.wait(t)
	assertCgroupRemoved(t, group)
	waitBackground(t, id, "running")
	if out := execSuccess(t, nil, name, "/bin/echo", "replacement-session"); out != "replacement-session\n" {
		t.Fatal("lost client prevented a replacement exec")
	}
}

func TestExecParallelSessions(t *testing.T) {
	name, id := execContainer(t, []string{"--pids-limit", "128"})
	first := startExec(t, "", nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo first-ready; sleep 1; echo first-done")
	firstGroup := execCgroup(t, first.ready(t, "first-ready\n"))
	second := startExec(t, "", nil, id, "/bin/sh", "-c", "cat /proc/self/cgroup; echo second-ready; sleep 1; echo second-done")
	secondGroup := execCgroup(t, second.ready(t, "second-ready\n"))
	if firstGroup == secondGroup {
		t.Fatal("parallel exec sessions shared their cleanup cgroup")
	}
	for _, call := range []*execInvocation{first, second} {
		if code, out, stderr := call.wait(t); code != 0 {
			t.Fatalf("parallel exec failed: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	}
	assertCgroupRemoved(t, firstGroup)
	assertCgroupRemoved(t, secondGroup)
	waitBackground(t, id, "running")
}

func TestExecRejectsUnavailableContainers(t *testing.T) {
	name := backgroundName(t)
	if code, out, _ := backgroundCLI(t, "exec", name, "--", "/bin/echo", "must-not-run"); code != 125 || out != "" {
		t.Fatal("exec ran against an unknown container")
	}
	id := startBackground(t, name, nil, "/bin/true")
	waitBackground(t, id, "exited")
	for _, reference := range []string{name, id} {
		if code, out, _ := backgroundCLI(t, "exec", reference, "--", "/bin/echo", "must-not-run"); code != 125 || out != "" {
			t.Fatalf("exec ran against an exited container: %s", reference)
		}
	}
}

func TestExecConcurrentStopAndRemove(t *testing.T) {
	name, id := execContainer(t, nil)
	call := startExec(t, "", nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo stop-ready; sleep 30")
	group := execCgroup(t, call.ready(t, "stop-ready\n"))
	if code, _, _ := backgroundCLI(t, "rm", id); code == 0 {
		t.Fatal("rm accepted a container with active exec")
	}
	backgroundSuccess(t, "stop", name)
	code, out, stderr := call.wait(t)
	if code == 0 {
		t.Fatalf("stop allowed an unfinished exec to succeed: stdout=%q stderr=%q", out, stderr)
	}
	assertCgroupRemoved(t, group)
	waitBackground(t, id, "exited")
	backgroundSuccess(t, "rm", id)
	if _, ok := findBackground(backgroundRecords(t, true), id); ok {
		t.Fatal("rm retained the stopped exec container")
	}
}

func TestExecContainerMainExitTerminatesSession(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "while [ ! -e /tmp/end-main ]; do sleep .01; done; sleep .2; exit 17")
	call := startExec(t, "", nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo main-exit-ready; sleep 30")
	group := execCgroup(t, call.ready(t, "main-exit-ready\n"))
	execSuccess(t, nil, id, "/bin/touch", "/tmp/end-main")
	code, out, stderr := call.wait(t)
	if code == 0 {
		t.Fatalf("main exit allowed ongoing exec to succeed: stdout=%q stderr=%q", out, stderr)
	}
	assertBackgroundExit(t, waitBackground(t, id, "exited"), 17)
	assertCgroupRemoved(t, group)
}

func TestExecSupervisorLossRecoversSession(t *testing.T) {
	name, id := execContainer(t, nil)
	record := waitBackground(t, id, "running")
	call := startExec(t, "", nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo supervisor-loss-ready; sleep 30")
	group := execCgroup(t, call.ready(t, "supervisor-loss-ready\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	output, err := exec.CommandContext(ctx, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", "mini-docker-"+id+".service").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("kill exec supervisor: %v: %s", err, output)
	}
	failed := waitBackground(t, id, "failed")
	if failed.ExitCode != nil || failed.Error == "" {
		t.Fatalf("supervisor loss guessed container command exit: %+v", failed)
	}
	code, out, stderr := call.wait(t)
	if code == 0 {
		t.Fatalf("supervisor loss produced successful exec exit: stdout=%q stderr=%q", out, stderr)
	}
	assertCgroupRemoved(t, group)
	if _, err := os.Stat(record.RunPath); !os.IsNotExist(err) {
		t.Fatalf("supervisor recovery retained exec runtime directory: %v", err)
	}
	assertBackgroundUnitStopped(t, id)
}

func TestExecRejectsStoppingContainer(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "trap '' TERM; echo stubborn-main-ready; sleep 30")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(backgroundSuccess(t, "logs", id), "stubborn-main-ready\n") {
		if time.Now().After(deadline) {
			t.Fatal("main did not install its stop trap")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() {
		code, out, stderr, err := backgroundCommand(ctx, "stop", name)
		if err == nil && code != 0 {
			err = fmt.Errorf("stop exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		stopped <- err
	}()
	waitBackground(t, id, "stopping")
	if code, out, _ := backgroundCLI(t, "exec", name, "--", "/bin/echo", "must-not-run"); code != 125 || out != "" {
		t.Fatal("exec accepted a stopping container")
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	waitBackground(t, id, "exited")
}
