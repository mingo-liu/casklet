//go:build linux

package integration

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type backgroundRecord struct {
	CleanupFailures []string   `json:"cleanup_failures"`
	Cgroup          string     `json:"cgroup"`
	ID              string     `json:"id"`
	Generation      uint64     `json:"generation"`
	Name            string     `json:"name"`
	State           string     `json:"state"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	ExitCode        *int       `json:"exit_code"`
	Error           string     `json:"error"`
	Command         []string   `json:"command"`
	LogTruncated    bool       `json:"log_truncated"`
	RunPath         string     `json:"run_path"`
}

func backgroundCommand(ctx context.Context, args ...string) (int, string, string, error) {
	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return -1, stdout.String(), stderr.String(), err
		}
		code = exit.ExitCode()
	}
	if ctx.Err() != nil {
		return code, stdout.String(), stderr.String(), ctx.Err()
	}
	return code, stdout.String(), stderr.String(), nil
}

func backgroundCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, out, stderr, err := backgroundCommand(ctx, args...)
	if err != nil {
		t.Fatalf("management command %q: %v stdout=%q stderr=%q", args, err, out, stderr)
	}
	return code, out, stderr
}

func backgroundSuccess(t *testing.T, args ...string) string {
	t.Helper()
	code, out, stderr := backgroundCLI(t, args...)
	if code != 0 {
		t.Fatalf("management command %q exit=%d stdout=%q stderr=%q", args, code, out, stderr)
	}
	return out
}

func backgroundName(t *testing.T) string {
	t.Helper()
	require(t)
	name := fmt.Sprintf("integration-%d-%d", os.Getpid(), sequence.Add(1))
	// Register cleanup before startup so failed launches and failed assertions do not leak units.
	t.Cleanup(func() {
		for _, operation := range []string{"stop", "rm"} {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, _, _, err := backgroundCommand(ctx, operation, name)
			cancel()
			if err != nil {
				t.Errorf("cleanup %s %s: %v", operation, name, err)
			}
		}
	})
	return name
}

func detachedArguments(name string, options []string, command ...string) []string {
	args := []string{"run", "--detach", "--name", name, "--rootfs", template}
	args = append(args, options...)
	args = append(args, "--")
	return append(args, command...)
}

func backgroundID(t *testing.T, output string) string {
	t.Helper()
	id := strings.TrimSpace(output)
	if decoded, err := hex.DecodeString(id); err != nil || len(decoded) != 16 || output != id+"\n" {
		t.Fatalf("detached startup must print only a full container ID: %q", output)
	}
	return id
}

func startBackground(t *testing.T, name string, options []string, command ...string) string {
	t.Helper()
	return backgroundID(t, backgroundSuccess(t, detachedArguments(name, options, command...)...))
}

func backgroundRecords(t *testing.T, all bool) []backgroundRecord {
	t.Helper()
	args := []string{"ps", "--json"}
	if all {
		args = append(args, "--all")
	}
	out := backgroundSuccess(t, args...)
	var records []backgroundRecord
	if err := json.Unmarshal([]byte(out), &records); err != nil || strings.TrimSpace(out) == "null" {
		t.Fatalf("ps must return a JSON array: %q: %v", out, err)
	}
	return records
}

func findBackground(records []backgroundRecord, reference string) (backgroundRecord, bool) {
	for _, record := range records {
		if record.ID == reference || record.Name == reference {
			return record, true
		}
	}
	return backgroundRecord{}, false
}

func waitBackground(t *testing.T, reference string, states ...string) backgroundRecord {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var latest backgroundRecord
	for time.Now().Before(deadline) {
		if record, ok := findBackground(backgroundRecords(t, true), reference); ok {
			latest = record
			for _, state := range states {
				if record.State == state {
					return record
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("container %s did not reach %v: %+v", reference, states, latest)
	return backgroundRecord{}
}

func assertBackgroundExit(t *testing.T, record backgroundRecord, expected int) {
	t.Helper()
	if record.State != "exited" || record.ExitCode == nil || *record.ExitCode != expected || record.StartedAt == nil || record.FinishedAt == nil {
		t.Fatalf("expected completed container exit=%d, got %+v", expected, record)
	}
	if record.CreatedAt.IsZero() || record.StartedAt.Before(record.CreatedAt) || record.FinishedAt.Before(*record.StartedAt) {
		t.Fatalf("invalid lifecycle timestamps: %+v", record)
	}
}

func assertBackgroundUnitStopped(t *testing.T, id string) {
	t.Helper()
	unit := "mini-docker-" + id + ".service"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=ActiveState", "--value").Output()
		if ctx.Err() != nil {
			t.Fatalf("background service remains active after terminal state: %s: %q", unit, out)
		}
		state := strings.TrimSpace(string(out))
		if err != nil || (state != "active" && state != "deactivating") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestBackgroundLifecycle(t *testing.T) {
	name := backgroundName(t)
	args := detachedArguments(name, nil, "/bin/sh", "-c", "echo ready; sleep 30")
	// Waiting for the scoped launcher must not wait for the background service.
	started := time.Now()
	code, out, stderr := start(t, "", args...).wait(t)
	if code != 0 {
		t.Fatalf("detached startup exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	id := backgroundID(t, out)
	if time.Since(started) > 10*time.Second {
		t.Fatal("detached launcher waited for the background command")
	}
	record := waitBackground(t, id, "running")
	if record.Name != name || len(record.Command) != 3 || record.Command[0] != "/bin/sh" {
		t.Fatalf("missing container identity or command: %+v", record)
	}
	if _, ok := findBackground(backgroundRecords(t, false), name); !ok {
		t.Fatal("running container is missing from default ps")
	}
	if code, out, _ := backgroundCLI(t, "__supervise", id); code != 125 || out != "" {
		t.Fatalf("internal supervisor accepted execution outside its dedicated unit: exit=%d stdout=%q", code, out)
	}
	waitBackground(t, id, "running")
	if code, _, _ := backgroundCLI(t, "rm", name); code == 0 {
		t.Fatal("rm removed an active container")
	}
	for _, reference := range []string{name, id} {
		if out := backgroundSuccess(t, "stop", reference); out != id+"\n" {
			t.Fatalf("stop must be idempotent and resolve ID/name: %q", out)
		}
	}
	record = waitBackground(t, id, "exited")
	if record.ExitCode == nil || record.FinishedAt == nil {
		t.Fatalf("stop did not preserve terminal status: %+v", record)
	}
	assertBackgroundUnitStopped(t, id)
	if _, ok := findBackground(backgroundRecords(t, false), id); ok {
		t.Fatal("default ps includes a terminal container")
	}
	backgroundSuccess(t, "rm", id)
	if _, ok := findBackground(backgroundRecords(t, true), name); ok {
		t.Fatal("rm retained the container record")
	}
	if code, _, _ := backgroundCLI(t, "logs", name); code == 0 {
		t.Fatal("logs remained accessible after rm")
	}
	newID := startBackground(t, name, nil, "/bin/echo", "name-reused")
	if newID == id {
		t.Fatal("new container reused an old ID")
	}
	assertBackgroundExit(t, waitBackground(t, newID, "exited"), 0)
}

func TestBackgroundLogs(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "printf 'first\\n'; sleep 1; printf 'second\\n' >&2; printf 'third\\n'")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	code, followed, stderr, err := backgroundCommand(ctx, "logs", "--follow", id)
	if err != nil || code != 0 || followed != "first\nsecond\nthird\n" {
		t.Fatalf("follow exit=%d stdout=%q stderr=%q error=%v", code, followed, stderr, err)
	}
	assertBackgroundExit(t, waitBackground(t, id, "exited"), 0)
	if out := backgroundSuccess(t, "logs", name); out != followed {
		t.Fatalf("persistent combined logs differ: %q", out)
	}
	if out := backgroundSuccess(t, "logs", "--tail", "2", name); out != "second\nthird\n" {
		t.Fatalf("tail returned incorrect lines: %q", out)
	}
	if out := backgroundSuccess(t, "logs", "--tail", "0", id); out != "" {
		t.Fatalf("zero tail returned data: %q", out)
	}
	if code, _, _ := backgroundCLI(t, "logs", "--tail", "-1", id); code == 0 {
		t.Fatal("negative log tail was accepted")
	}
}

func TestBackgroundLogBound(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "dd if=/dev/zero bs=1048576 count=17 2>/dev/null")
	record := waitBackground(t, id, "exited")
	assertBackgroundExit(t, record, 0)
	out := backgroundSuccess(t, "logs", name)
	if len(out) > 16*1024*1024 || len(out) < 12*1024*1024 || strings.Trim(out, "\x00") != "" || !record.LogTruncated {
		t.Fatalf("logs must retain recent bounded output without blocking: bytes=%d discarded=%v", len(out), record.LogTruncated)
	}

}

func TestBackgroundFollowCancellation(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "echo ready; sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stdout, err := os.CreateTemp(t.TempDir(), "follow-stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, binary, "logs", "-f", id)
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	waited := false
	defer func() {
		cancel()
		if !waited {
			select {
			case <-finished:
			case <-time.After(time.Second):
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := os.ReadFile(stdout.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "ready\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("follow did not stream existing output")
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		waited = true
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 130 {
			t.Fatalf("follow cancellation error=%v stderr=%q", err, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("follow did not stop after SIGINT")
	}
	waitBackground(t, id, "running")
}

func TestBackgroundStopAndExecutionOptions(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, []string{"--user", "65534:65534", "--read-only", "--env", "BACKGROUND_VALUE=isolated", "--workdir", "/tmp", "--cpus", "0.5"}, "/bin/sh", "-c", `
set -eu
[ "$(id -u)" = 65534 ]
[ "$(id -g)" = 65534 ]
[ "$BACKGROUND_VALUE" = isolated ]
[ "$(pwd)" = /tmp ]
if touch /rootfs-must-be-read-only 2>/dev/null; then exit 9; fi
touch /tmp/writable
trap 'echo stopped; exit 0' TERM
echo ready
while :; do sleep 1; done`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		out := backgroundSuccess(t, "logs", id)
		if strings.Contains(out, "ready\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("configured background command did not become ready: %q", out)
		}
		time.Sleep(25 * time.Millisecond)
	}
	backgroundSuccess(t, "stop", name)
	assertBackgroundExit(t, waitBackground(t, id, "exited"), 0)
	if out := backgroundSuccess(t, "logs", id); !strings.Contains(out, "stopped\n") {
		t.Fatalf("TERM trap did not run: %q", out)
	}
	assertBackgroundUnitStopped(t, id)
}

func TestBackgroundBoundedStop(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "trap '' TERM; echo ready; while :; do sleep 30; done")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(backgroundSuccess(t, "logs", id), "ready\n") {
		if time.Now().After(deadline) {
			t.Fatal("stubborn container did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	started := time.Now()
	backgroundSuccess(t, "stop", id)
	if time.Since(started) > 10*time.Second {
		t.Fatal("stubborn container stop exceeded its bound")
	}
	record := waitBackground(t, id, "exited")
	if record.ExitCode == nil || *record.ExitCode == 0 {
		t.Fatalf("forced stop lost its failure status: %+v", record)
	}
	assertBackgroundUnitStopped(t, id)
}

func TestBackgroundTerminalResults(t *testing.T) {
	t.Run("startup-failure", func(t *testing.T) {
		name := backgroundName(t)
		code, out, stderr := backgroundCLI(t, detachedArguments(name, nil, "/bin/does-not-exist")...)
		if code != 125 || out != "" {
			t.Fatalf("startup failure published an ID: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		record := waitBackground(t, name, "failed")
		if record.StartedAt != nil || record.FinishedAt == nil || record.Error == "" {
			t.Fatalf("startup failure was not retained accurately: %+v", record)
		}
		assertBackgroundUnitStopped(t, record.ID)
	})
	t.Run("fast-exit", func(t *testing.T) {
		name := backgroundName(t)
		id := startBackground(t, name, nil, "/bin/sh", "-c", "echo complete; exit 7")
		assertBackgroundExit(t, waitBackground(t, id, "exited"), 7)
		if out := backgroundSuccess(t, "logs", name); out != "complete\n" {
			t.Fatalf("fast command lost its logs: %q", out)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		name := backgroundName(t)
		id := startBackground(t, name, []string{"--timeout", "500ms"}, "/bin/sleep", "30")
		assertBackgroundExit(t, waitBackground(t, id, "exited"), 124)
		assertBackgroundUnitStopped(t, id)
	})
	t.Run("stdin-eof", func(t *testing.T) {
		name := backgroundName(t)
		code, out, stderr := start(t, "must not reach the container\n", detachedArguments(name, nil, "/bin/cat")...).wait(t)
		if code != 0 {
			t.Fatalf("stdin EOF launcher exit=%d stderr=%q", code, stderr)
		}
		id := backgroundID(t, out)
		assertBackgroundExit(t, waitBackground(t, id, "exited"), 0)
		if out := backgroundSuccess(t, "logs", id); out != "" {
			t.Fatalf("background stdin inherited launcher input: %q", out)
		}
	})
}

func TestBackgroundConcurrentNames(t *testing.T) {
	first, second := backgroundName(t), backgroundName(t)
	type result struct {
		name, stdout, stderr string
		code                 int
		err                  error
	}
	results := make(chan result, 2)
	for _, name := range []string{first, second} {
		go func(name string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			code, out, stderr, err := backgroundCommand(ctx, detachedArguments(name, nil, "/bin/sleep", "30")...)
			results <- result{name, out, stderr, code, err}
		}(name)
	}
	ids := make(map[string]bool)
	// Drain both launchers before reporting a failure so cleanup cannot race a
	// second launcher that has not published its background service yet.
	completed := []result{<-results, <-results}
	for _, item := range completed {
		if item.err != nil || item.code != 0 {
			t.Fatalf("concurrent launch %s exit=%d stdout=%q stderr=%q error=%v", item.name, item.code, item.stdout, item.stderr, item.err)
		}
		id := backgroundID(t, item.stdout)
		if ids[id] {
			t.Fatalf("concurrent launches reused ID %s", id)
		}
		ids[id] = true
		waitBackground(t, item.name, "running")
	}
	code, out, stderr := backgroundCLI(t, detachedArguments(first, nil, "/bin/echo", "collision")...)
	if code == 0 || out != "" {
		t.Fatalf("duplicate name was accepted: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if record := waitBackground(t, first, "running"); !ids[record.ID] {
		t.Fatalf("duplicate launch replaced existing container: %+v", record)
	}
}

func TestBackgroundConcurrentManagement(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/sh", "-c", "trap 'exit 0' TERM; echo ready; while :; do sleep 1; done")
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(backgroundSuccess(t, "logs", id), "ready\n") {
		if time.Now().After(deadline) {
			t.Fatal("container did not become ready for concurrent stop")
		}
		time.Sleep(25 * time.Millisecond)
	}
	type result struct {
		stdout, stderr string
		code           int
		err            error
	}
	results := make(chan result, 2)
	for _, reference := range []string{name, id} {
		go func(reference string) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			code, out, stderr, err := backgroundCommand(ctx, "stop", reference)
			results <- result{out, stderr, code, err}
		}(reference)
	}
	for i := 0; i < 5; i++ {
		if _, ok := findBackground(backgroundRecords(t, true), id); !ok {
			t.Fatal("concurrent stop discarded its record")
		}
	}
	for i := 0; i < 2; i++ {
		item := <-results
		if item.err != nil || item.code != 0 || item.stdout != id+"\n" {
			t.Fatalf("concurrent stop exit=%d stdout=%q stderr=%q error=%v", item.code, item.stdout, item.stderr, item.err)
		}
	}
	assertBackgroundExit(t, waitBackground(t, id, "exited"), 0)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		code, out, stderr, err := backgroundCommand(ctx, "rm", name)
		results <- result{out, stderr, code, err}
	}()
	for i := 0; i < 10; i++ {
		backgroundRecords(t, true)
	}
	item := <-results
	if item.err != nil || item.code != 0 {
		t.Fatalf("concurrent removal exit=%d stdout=%q stderr=%q error=%v", item.code, item.stdout, item.stderr, item.err)
	}
	if _, ok := findBackground(backgroundRecords(t, true), id); ok {
		t.Fatal("completed concurrent removal retained its record")
	}
}

func TestBackgroundSupervisorDeathRecovery(t *testing.T) {
	name := backgroundName(t)
	before, err := os.ReadDir("/var/lib/mini-docker/runs")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	existing := make(map[string]bool)
	for _, entry := range before {
		existing[entry.Name()] = true
	}
	id := startBackground(t, name, nil, "/bin/sh", "-c", "echo ready; sleep 30")
	waitBackground(t, id, "running")
	unit := "mini-docker-" + id + ".service"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	output, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=ControlGroup", "--value").Output()
	cancel()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		t.Fatalf("read service cgroup: %v: %q", err, output)
	}
	group := filepath.Join("/sys/fs/cgroup", strings.TrimSpace(string(output)))
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	output, err = exec.CommandContext(ctx, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", unit).CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("kill supervisor: %v: %s", err, output)
	}
	record := waitBackground(t, id, "failed")
	if record.ExitCode != nil || record.Error == "" || record.FinishedAt == nil {
		t.Fatalf("lost supervisor did not retain an unknown command exit: %+v", record)
	}
	assertBackgroundUnitStopped(t, id)
	assertCgroupRemoved(t, group)
	entries, err := os.ReadDir("/var/lib/mini-docker/runs")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !existing[entry.Name()] {
			t.Errorf("supervisor recovery left runtime directory %s", entry.Name())
		}
	}
	// A subsequent launch must work after recovery without losing the retained record.
	other := backgroundName(t)
	otherID := startBackground(t, other, nil, "/bin/echo", "recovered")
	assertBackgroundExit(t, waitBackground(t, otherID, "exited"), 0)
	if _, ok := findBackground(backgroundRecords(t, true), name); !ok {
		t.Fatal("recovery discarded the failed container record")
	}
}

func TestBackgroundPreviousBootReconciliation(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, nil, "/bin/true")
	assertBackgroundExit(t, waitBackground(t, id, "exited"), 0)
	assertBackgroundUnitStopped(t, id)

	// Rewrite only this test's stopped state to model an interrupted startup
	// from a previous boot. A recent timestamp must not grant it scheduling grace.
	dir := filepath.Join("/var/lib/mini-docker/containers", id)
	// An interrupted execution has no receipt. Keeping the real completed
	// execution's receipt would correctly recover its known exit status instead.
	if err := os.Remove(filepath.Join(dir, "exit-0.json")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record["state"] = "starting"
	record["boot_id"] = "00000000-0000-0000-0000-000000000000"
	record["created_at"] = time.Now().UTC()
	for _, key := range []string{"started_at", "finished_at", "exit_code", "error", "run_path", "cgroup"} {
		delete(record, key)
	}
	data, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(dir, ".previous-boot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		t.Fatal(err)
	}
	updated, ok := findBackground(backgroundRecords(t, true), id)
	if !ok || updated.State != "failed" || updated.ExitCode != nil || updated.FinishedAt == nil || updated.Error == "" {
		t.Fatalf("previous-boot startup was not reconciled immediately: present=%v record=%+v", ok, updated)
	}
}

func TestBackgroundCanceledStartup(t *testing.T) {
	name := backgroundName(t)
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
	args := detachedArguments(name, nil, "/bin/echo", "command-must-not-start")
	for i, arg := range args {
		if arg == "--rootfs" {
			args[i+1] = source
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Error("canceled startup launcher did not terminate during cleanup")
			}
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	var interrupted backgroundRecord
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir("/var/lib/mini-docker/containers")
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, entry := range entries {
			decoded, err := hex.DecodeString(entry.Name())
			if err != nil || len(decoded) != 16 {
				continue
			}
			data, err := os.ReadFile(filepath.Join("/var/lib/mini-docker/containers", entry.Name(), "state.json"))
			if err != nil {
				continue // Records are published and replaced atomically.
			}
			var record backgroundRecord
			if err := json.Unmarshal(data, &record); err != nil || record.Name != name || record.RunPath == "" {
				continue
			}
			stages, err := filepath.Glob(filepath.Join("/var/lib/mini-docker/containers", record.ID, ".rootfs-*"))
			if err != nil || len(stages) != 1 {
				continue
			}
			copied := filepath.Join(stages[0], "startup-payload")
			if _, err := os.Stat(filepath.Join(copied, "payload-00000")); err != nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(copied, "zz-finished")); !os.IsNotExist(err) {
				continue
			}
			if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			interrupted = record
			break
		}
		if interrupted.ID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if interrupted.ID == "" {
		t.Fatal("did not observe detached rootfs preparation before completion")
	}
	select {
	case err := <-finished:
		waited = true
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 130 || stdout.Len() != 0 {
			t.Fatalf("canceled detached startup error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("canceled detached startup did not finish rollback within its bound")
	}
	record := waitBackground(t, name, "failed")
	if record.StartedAt != nil || record.FinishedAt == nil {
		t.Fatalf("canceled startup incorrectly recorded command execution: %+v", record)
	}
	if out := backgroundSuccess(t, "logs", name); strings.Contains(out, "command-must-not-start") {
		t.Fatalf("canceled command ran: %q", out)
	}
	if _, err := os.Stat(interrupted.RunPath); !os.IsNotExist(err) {
		t.Fatalf("canceled startup left its runtime directory: %v", err)
	}
	assertBackgroundUnitStopped(t, interrupted.ID)
}

func TestBackgroundLogRotationFollow(t *testing.T) {
	name := backgroundName(t)
	id := startBackground(t, name, []string{"--log-max-size", "1k", "--log-max-files", "3"}, "/bin/sh", "-c", `echo ready; sleep 1; i=0; while [ "$i" -lt 10 ]; do printf '%0900d\n' "$i"; i=$((i+1)); sleep .15; done; printf final`)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	code, out, stderr, err := backgroundCommand(ctx, "logs", "-f", id)
	var want strings.Builder
	want.WriteString("ready\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&want, "%0900d\n", i)
	}
	want.WriteString("final")
	if err != nil || code != 0 || out != want.String() {
		t.Fatalf("rotated follow: code=%d bytes=%d want=%d stderr=%q error=%v", code, len(out), want.Len(), stderr, err)
	}
	assertBackgroundExit(t, waitBackground(t, id, "exited"), 0)
	retained := backgroundSuccess(t, "logs", id)
	if len(retained) > 3072 || !strings.HasSuffix(want.String(), retained) || !strings.HasSuffix(retained, "final") {
		t.Fatalf("retained rotation: %d bytes", len(retained))
	}
	tail := backgroundSuccess(t, "logs", "--tail", "2", id)
	if tail != fmt.Sprintf("%0900d\nfinal", 9) {
		t.Fatal("tail failed across segments")
	}
	inspect := backgroundSuccess(t, "inspect", id)
	if !strings.Contains(inspect, `"log_max_size": 1024`) || !strings.Contains(inspect, `"log_max_files": 3`) {
		t.Fatalf("retention missing from inspection: %s", inspect)
	}
}
