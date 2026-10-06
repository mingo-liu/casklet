//go:build linux

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func lifecycleReady(t *testing.T, id, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(backgroundSuccess(t, "logs", id), marker) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("container did not log %q: %s", marker, backgroundSuccess(t, "logs", id))
}

func lifecycleWait(t *testing.T, id string, expected int) {
	t.Helper()
	code, out, stderr := backgroundCLI(t, "wait", id)
	if code != expected || out != fmt.Sprintf("%d\n", expected) || stderr != "" {
		t.Fatalf("wait exit=%d stdout=%q stderr=%q, want %d", code, out, stderr, expected)
	}
}

func TestLifecycleRetainedFilesystem(t *testing.T) {
	name := backgroundName(t)
	source := t.TempDir()
	if out, err := exec.Command("cp", "-a", template+"/.", source).CombinedOutput(); err != nil {
		t.Fatalf("copy: %v %s", err, out)
	}
	data := t.TempDir()
	command := `
[ ! -e /tmp/ephemeral ] || exit 90
n=0; [ ! -e /counter ] || n=$(cat /counter)
n=$((n+1)); echo "$n" > /counter; echo "$n" > /data/counter
if [ "$n" -eq 1 ]; then touch /owned; chmod 600 /owned
else [ "$(stat -c '%u:%g:%a' /owned)" = '1234:2345:600' ] || exit 91; fi
touch /tmp/ephemeral
trap 'sleep .2; exit 17' TERM
echo "execution-$n"
while :; do sleep .1; done`
	id := startBackground(t, name, []string{"--rootfs", source, "--stop-timeout", "50ms", "--mount", "type=bind,source=" + data + ",target=/data"}, "/bin/sh", "-c", command)
	lifecycleReady(t, id, "execution-1\n")
	if err := os.Chown(filepath.Join("/var/lib/mini-docker/containers", id, "rootfs", "owned"), 1234, 2345); err != nil {
		t.Fatal(err)
	}
	original := inspectBackground(t, id)
	// Starting a running execution must not mutate it.
	if out := backgroundSuccess(t, "start", name); out != id+"\n" {
		t.Fatal(out)
	}
	if got := inspectBackground(t, id); got.Generation != 0 || got.StartedAt == nil || !got.StartedAt.Equal(*original.StartedAt) {
		t.Fatalf("idempotent start changed execution: %+v", got)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	// A waiter must receive generation zero's graceful exit even after restart.
	waiter := startLifecycleWait(t, name)
	time.Sleep(200 * time.Millisecond)
	oldExec := startExec(t, "", nil, id, "/bin/sh", "-c", "echo old-exec; sleep 30")
	oldExec.ready(t, "old-exec\n")
	if out := backgroundSuccess(t, "restart", "--timeout", "2s", name); out != id+"\n" {
		t.Fatal(out)
	}
	waiter.check(t, 17, "17\n")
	if code, _, _ := oldExec.wait(t); code == 0 {
		t.Fatal("old exec survived restart")
	}
	lifecycleReady(t, id, "execution-2\n")
	got := inspectBackground(t, name)
	if got.Generation != 1 || got.ID != id || got.CreatedAt != original.CreatedAt || got.ExitCode != nil || got.PreviousExit == nil || got.PreviousExit.ExitCode == nil || *got.PreviousExit.ExitCode != 17 || got.PreviousExit.Generation != 0 || !got.FilesystemRetained {
		t.Fatalf("invalid restarted inspection: %+v", got)
	}
	if code, out, stderr := startExec(t, "", nil, name, "/bin/cat", "/counter").wait(t); code != 0 || out != "2\n" {
		t.Fatalf("new exec exit=%d out=%q stderr=%q", code, out, stderr)
	}
	backgroundSuccess(t, "stop", "--timeout", "2s", id)
	lifecycleWait(t, id, 17)
	lifecycleWait(t, name, 17)
	backgroundSuccess(t, "start", id)
	lifecycleReady(t, id, "execution-3\n")
	backgroundSuccess(t, "stop", "--timeout", "2s", id)
	lifecycleWait(t, id, 17)
	if content, err := os.ReadFile(filepath.Join(data, "counter")); err != nil || string(content) != "3\n" {
		t.Fatalf("bind data: %q %v", content, err)
	}
	logs := backgroundSuccess(t, "logs", id)
	for _, marker := range []string{"execution-1\n", "execution-2\n", "execution-3\n"} {
		if strings.Count(logs, marker) != 1 {
			t.Fatalf("missing appended logs: %q", logs)
		}
	}
	path := filepath.Join("/var/lib/mini-docker/containers", id)
	for gen := 0; gen < 3; gen++ {
		if _, err := os.Stat(filepath.Join(path, fmt.Sprintf("exit-%d.json", gen))); err != nil {
			t.Fatal(err)
		}
	}
	backgroundSuccess(t, "rm", name)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rm retained rootfs/receipts: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(data, "counter")); err != nil || string(content) != "3\n" {
		t.Fatalf("rm changed bind data: %q %v", content, err)
	}
}

func TestLifecycleStopDeadlines(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(fmt.Sprint(override), func(t *testing.T) {
			name := backgroundName(t)
			id := startBackground(t, name, []string{"--stop-timeout", "200ms"}, "/bin/sh", "-c", "trap '' TERM; echo stubborn-ready; while :; do sleep 30; done")
			lifecycleReady(t, id, "stubborn-ready\n")
			args := []string{"stop", id}
			if override {
				args = []string{"stop", "--timeout", "0s", id}
			}
			began := time.Now()
			backgroundSuccess(t, args...)
			if elapsed := time.Since(began); elapsed > 3*time.Second {
				t.Fatalf("shutdown exceeded selected grace: %v", elapsed)
			}
			lifecycleWait(t, id, 137)
			backgroundSuccess(t, "stop", id)
			lifecycleWait(t, id, 137)
			got := inspectBackground(t, id)
			if got.Config.StopTimeout != "200ms" {
				t.Fatalf("override changed saved default: %+v", got.Config)
			}
		})
	}
}

type lifecycleWaitCall struct {
	cmd         *exec.Cmd
	out, stderr *os.File
	done        chan error
}

func startLifecycleWait(t *testing.T, ref string) *lifecycleWaitCall {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	out, err := os.CreateTemp(t.TempDir(), "wait-out-")
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.CreateTemp(t.TempDir(), "wait-err-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "wait", ref)
	cmd.Stdout, cmd.Stderr = out, stderr
	call := &lifecycleWaitCall{cmd: cmd, out: out, stderr: stderr, done: make(chan error, 1)}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { call.done <- cmd.Wait() }()
	t.Cleanup(func() { cancel(); out.Close(); stderr.Close() })
	return call
}
func (call *lifecycleWaitCall) check(t *testing.T, expected int, output string) {
	t.Helper()
	select {
	case <-call.done:
	case <-time.After(8 * time.Second):
		t.Fatal("wait did not complete")
	}
	out, _ := os.ReadFile(call.out.Name())
	stderr, _ := os.ReadFile(call.stderr.Name())
	if call.cmd.ProcessState.ExitCode() != expected || string(out) != output || len(stderr) != 0 {
		t.Fatalf("wait exit=%d stdout=%q stderr=%q", call.cmd.ProcessState.ExitCode(), out, stderr)
	}
}

func TestLifecycleWaitResults(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "sleep .3; exit 7")
	lifecycleWait(t, name, 7)
	lifecycleWait(t, id, 7)
	backgroundSuccess(t, "start", id)
	lifecycleWait(t, id, 7)
	failed := backgroundName(t)
	code, _, _ := backgroundCLI(t, detachedArguments(failed, nil, "/missing-command")...)
	if code != 125 {
		t.Fatalf("failed launch exit=%d", code)
	}
	lifecycleWait(t, failed, 125)
	canceled := backgroundName(t)
	active := startBackground(t, canceled, nil, "/bin/sleep", "30")
	call := startLifecycleWait(t, canceled)
	time.Sleep(200 * time.Millisecond)
	if err := call.cmd.Process.Signal(unix.SIGINT); err != nil {
		t.Fatal(err)
	}
	call.check(t, 130, "")
	waitBackground(t, active, "running")
}

func TestLifecycleFailureRetry(t *testing.T) {
	name := backgroundName(t)
	source := t.TempDir()
	code, _, _ := backgroundCLI(t, detachedArguments(name, []string{"--mount", "type=bind,source=" + source + ",target=/app"}, "/app/command")...)
	if code != 125 {
		t.Fatalf("initial missing command exit=%d", code)
	}
	record := waitBackground(t, name, "failed")
	lifecycleWait(t, name, 125)
	if err := os.WriteFile(filepath.Join(source, "command"), []byte("#!/bin/sh\necho retried\nexit 9\n"), 0755); err != nil {
		t.Fatal(err)
	}
	backgroundSuccess(t, "start", name)
	lifecycleWait(t, record.ID, 9)
	got := inspectBackground(t, name)
	if got.Generation != 1 || got.PreviousExit == nil || got.PreviousExit.ExitCode == nil || *got.PreviousExit.ExitCode != 125 {
		t.Fatalf("lost failed receipt: %+v", got)
	}
	if out := backgroundSuccess(t, "logs", name); !strings.Contains(out, "retried\n") {
		t.Fatal(out)
	}
}

func TestLifecycleConcurrentStart(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "trap 'exit 19' TERM; echo concurrent-ready; while :; do sleep .1; done")
	lifecycleReady(t, id, "concurrent-ready\n")
	backgroundSuccess(t, "stop", id)
	lifecycleWait(t, id, 19)
	type result struct {
		code        int
		out, stderr string
		err         error
	}
	results := make(chan result, 8)
	for i := 0; i < 8; i++ {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, o, s, e := backgroundCommand(ctx, "start", name)
			results <- result{c, o, s, e}
		}()
	}
	successes := 0
	for i := 0; i < 8; i++ {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.code == 0 {
			successes++
			if r.out != id+"\n" {
				t.Fatal(r)
			}
		} else if r.code != 125 || !strings.Contains(r.stderr, "execution changed") {
			t.Fatalf("unexpected concurrent result: %+v", r)
		}
	}
	if successes == 0 {
		t.Fatal("no start succeeded")
	}
	if got := inspectBackground(t, id); got.Generation != 1 || got.State != "running" {
		t.Fatalf("concurrent start duplicated execution: %+v", got)
	}
	lifecycleReady(t, id, "concurrent-ready\n")
	waiter := startLifecycleWait(t, id)
	time.Sleep(200 * time.Millisecond)
	backgroundSuccess(t, "stop", id)
	waiter.check(t, 19, "19\n")
}

func TestLifecycleRetainedRootSafety(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/true")
	lifecycleWait(t, id, 0)
	root := filepath.Join("/var/lib/mini-docker/containers", id, "rootfs")
	original := root + "-saved"
	if err := os.Rename(root, original); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := backgroundCLI(t, "start", id); code != 125 {
		t.Fatal("start accepted retained rootfs symlink")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, root); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(outside, root, "", unix.MS_BIND, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Unmount(root, unix.MNT_DETACH) })
	for _, command := range []string{"start", "rm"} {
		if code, _, _ := backgroundCLI(t, command, id); code != 125 {
			t.Fatalf("%s accepted retained mounted root", command)
		}
	}
	if err := unix.Unmount(root, unix.MNT_DETACH); err != nil {
		t.Fatal(err)
	}
	backgroundSuccess(t, "rm", id)
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "safe" {
		t.Fatalf("outside data changed: %q %v", content, err)
	}
}

func TestLifecycleLogBoundAcrossStarts(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "dd if=/dev/zero bs=1048576 count=17 2>/dev/null")
	lifecycleWait(t, id, 0)
	path := filepath.Join("/var/lib/mini-docker/containers", id, "container.log")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	backgroundSuccess(t, "start", id)
	lifecycleWait(t, id, 0)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || after.Size() > 16*1024*1024 || !inspectBackground(t, id).LogTruncated {
		t.Fatalf("logs exceeded cumulative cap: %d -> %d", before.Size(), after.Size())
	}
}

func TestLifecycleSupervisorRecovery(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", `n=0; [ ! -e /recovery-counter ] || n=$(cat /recovery-counter); n=$((n+1)); echo "$n" > /recovery-counter; trap 'exit 23' TERM; echo "recovery-$n"; while :; do sleep .1; done`)
	lifecycleReady(t, id, "recovery-1\n")
	unit := "mini-docker-" + id + ".service"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	out, err := exec.CommandContext(ctx, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", unit).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("kill supervisor: %v %s", err, out)
	}
	failed := waitBackground(t, id, "failed")
	if failed.ExitCode != nil {
		t.Fatalf("recovery invented command status: %+v", failed)
	}
	code, stdout, stderr := backgroundCLI(t, "wait", id)
	if code != 125 || stdout != "" || !strings.Contains(stderr, "exit status is unknown") {
		t.Fatalf("unknown wait exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	backgroundSuccess(t, "start", name)
	lifecycleReady(t, id, "recovery-2\n")
	got := inspectBackground(t, id)
	if got.Generation != 1 || got.PreviousExit == nil || got.PreviousExit.ExitCode != nil {
		t.Fatalf("lost unknown exit receipt: %+v", got)
	}
	backgroundSuccess(t, "stop", id)
	lifecycleWait(t, id, 23)
}

func TestLifecycleImageAndStats(t *testing.T) {
	require(t)
	source := imageTemplate(t)
	imageID := importImage(t, source)
	name := backgroundName(t)
	id := startImageContainer(t, name, imageID, "/bin/sh", "-c", `n=0; [ ! -e /image-counter ] || n=$(cat /image-counter); n=$((n+1)); echo "$n" > /image-counter; trap 'exit 0' TERM; echo "image-$n"; while :; do sleep .1; done`)
	lifecycleReady(t, id, "image-1\n")
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	backgroundSuccess(t, "restart", id)
	lifecycleReady(t, id, "image-2\n")
	got := statsBackground(t, id)
	if got.State != "running" || got.MemoryBytes == nil || got.CPUPercent == nil {
		t.Fatalf("restarted metrics unavailable: %+v", got)
	}
	assertImageInUse(t, imageID)
	backgroundSuccess(t, "stop", id)
	lifecycleWait(t, id, 0)
	assertImageInUse(t, imageID)
	backgroundSuccess(t, "rm", id)
	backgroundSuccess(t, "image", "rm", imageID)
}
