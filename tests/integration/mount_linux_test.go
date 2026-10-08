//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"golang.org/x/sys/unix"
)

func bindOption(source, target string, readonly bool) string {
	value := "type=bind,source=" + source + ",target=" + target
	if readonly {
		value += ",readonly"
	}
	return value
}

func assertSourceFile(t *testing.T, source, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(source, name))
	if err != nil || string(data) != want {
		t.Fatalf("source file %s: content=%q error=%v", name, data, err)
	}
}

func TestBindMountPersistence(t *testing.T) {
	require(t)
	source := t.TempDir()
	root := executionTemplate(t)
	writeTemplateFile(t, root, "data/nested/result", "template-original", 0644)
	options := []string{"--rootfs", root, "--mount", bindOption(source, "/data/nested", false), "--read-only", "--workdir", "/data/nested"}
	success(t, options, "/bin/sh", "-c", "printf first > result; printf temporary > /tmp/ephemeral")
	assertSourceFile(t, source, "result", "first")
	success(t, options, "/bin/sh", "-c", "[ ! -e /tmp/ephemeral ] && [ \"$(cat result)\" = first ] && printf second >> result")
	assertSourceFile(t, source, "result", "firstsecond")
	assertSourceFile(t, filepath.Join(root, "data/nested"), "result", "template-original")
	success(t, nil, "/bin/sh", "-c", "[ ! -e /data/nested ]")
	// Mounting below /tmp must survive the runtime's tmpfs setup.
	success(t, []string{"--mount", bindOption(source, "/tmp/data", false)}, "/bin/sh", "-c", "[ \"$(cat /tmp/data/result)\" = firstsecond ]")
}

func TestBindMountReadOnlyAndNumericUser(t *testing.T) {
	require(t)
	source := t.TempDir()
	if err := os.Chmod(source, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "existing"), []byte("original"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(source, "existing"), 0666); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"0", "1000:1234"} {
		success(t, []string{"--user", user, "--mount", bindOption(source, "/data", true)}, "/bin/sh", "-c", `
set -eu
[ "$(cat /data/existing)" = original ]
if (echo changed > /data/existing) 2>/dev/null; then exit 1; fi
if touch /data/new 2>/dev/null; then exit 1; fi
if rm /data/existing 2>/dev/null; then exit 1; fi
if mkdir /data/newdir 2>/dev/null; then exit 1; fi
if mount -o remount,rw /data 2>/dev/null; then exit 1; fi
awk '$5 == "/data" { print $6 }' /proc/self/mountinfo | grep '^ro,'
[ "$(id -u)" != 0 ] || touch /root-writable
touch /tmp/writable`)
	}
	assertSourceFile(t, source, "existing", "original")
	if err := os.WriteFile(filepath.Join(source, "host-write"), []byte("writable"), 0600); err != nil {
		t.Fatal("read-only bind changed the host source mount:", err)
	}
	success(t, []string{"--user", "1000:1234", "--read-only", "--mount", bindOption(source, "/data", false)}, "/bin/sh", "-c", "printf nonroot > /data/owned")
	info, err := os.Stat(filepath.Join(source, "owned"))
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != 1000 || stat.Gid != 1234 {
		t.Fatalf("bind file owner=%d:%d", stat.Uid, stat.Gid)
	}
}

func TestBindMountIsolationAndExec(t *testing.T) {
	require(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "existing"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	writer, reader, isolated := backgroundName(t), backgroundName(t), backgroundName(t)
	id := startBackground(t, writer, []string{"--read-only", "--mount", bindOption(source, "/data dir", false)}, "/bin/sleep", "300")
	startBackground(t, reader, []string{"--mount", bindOption(source, "/shared", true)}, "/bin/sleep", "300")
	startBackground(t, isolated, nil, "/bin/sleep", "300")
	backgroundSuccess(t, "exec", writer, "--", "/bin/sh", "-c", "printf shared > '/data dir/result'")
	backgroundSuccess(t, "exec", reader, "--", "/bin/sh", "-c", "[ \"$(cat /shared/result)\" = shared ]; if touch /shared/denied; then exit 1; fi")
	backgroundSuccess(t, "exec", isolated, "--", "/bin/sh", "-c", "[ ! -e '/data dir' ] && [ ! -e /shared ]")
	var inspection container.Inspection
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "inspect", id)), &inspection); err != nil {
		t.Fatal(err)
	}
	if len(inspection.Config.Mounts) != 1 || inspection.Config.Mounts[0].Source != source || inspection.Config.Mounts[0].Target != "/data dir" || inspection.Config.Mounts[0].ReadOnly {
		t.Fatalf("inspection mounts=%+v", inspection.Config.Mounts)
	}
	for _, name := range []string{writer, reader, isolated} {
		backgroundSuccess(t, "stop", name)
		backgroundSuccess(t, "rm", name)
	}
	assertSourceFile(t, source, "result", "shared")
	assertSourceFile(t, source, "existing", "original")
	after, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("container bind mounts changed the host mount namespace")
	}
}

func TestBindMountValidationAndRollback(t *testing.T) {
	require(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "keep"), []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	root := executionTemplate(t)
	if err := os.Symlink("/tmp", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	writeTemplateFile(t, root, "file", "template-file", 0644)
	// Fail the second mount after the first bind has already succeeded.
	for _, target := range []string{"/escape", "/escape/child", "/file", "/file/child"} {
		code, out, stderr := run(t, []string{"--rootfs", root, "--mount", bindOption(source, "/first", false), "--mount", bindOption(source, target, true)}, "/bin/echo", "must-not-run")
		if code != 125 || out != "" || !strings.Contains(stderr, "mount target") {
			t.Fatalf("unsafe target %s: exit=%d stdout=%q stderr=%q", target, code, out, stderr)
		}
		assertSourceFile(t, source, "keep", "preserved")
	}
	link := filepath.Join(t.TempDir(), "source-link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{link, link + "/child", source + "/keep", source + "/missing", root} {
		code, out, stderr := run(t, []string{"--rootfs", root, "--mount", bindOption(invalid, "/data", false)}, "/bin/echo", "must-not-run")
		if code != 125 || out != "" {
			t.Fatalf("invalid source %s: exit=%d stdout=%q stderr=%q", invalid, code, out, stderr)
		}
	}
	for _, options := range [][]string{
		{"--workdir", "/missing"}, {"--user", "1000"},
	} {
		args := append([]string{"--rootfs", root, "--mount", bindOption(source, "/data", false)}, options...)
		code, _, _ := run(t, args, "/missing-command")
		if code != 125 {
			t.Fatalf("late startup error exit=%d", code)
		}
		assertSourceFile(t, source, "keep", "preserved")
	}
	name := backgroundName(t)
	code, _, stderr := backgroundCLI(t, detachedArguments(name, []string{"--rootfs", root, "--mount", bindOption(source, "/first", false), "--mount", bindOption(source, "/escape/child", true)}, "/bin/echo", "must-not-run")...)
	if code != 125 {
		t.Fatalf("detached rollback exit=%d stderr=%q", code, stderr)
	}
	record := waitBackground(t, name, "failed")
	assertBackgroundUnitStopped(t, record.ID)
	backgroundSuccess(t, "rm", name)
	assertSourceFile(t, source, "keep", "preserved")
	success(t, []string{"--mount", bindOption(source, "/data", false)}, "/bin/sh", "-c", "[ \"$(cat /data/keep)\" = preserved ]")
}

func TestBindMountExcludesSubmounts(t *testing.T) {
	require(t)
	source := t.TempDir()
	child := filepath.Join(source, "child")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "underlying"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", child, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=1m"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(child, 0); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(filepath.Join(child, "host-mount"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	success(t, []string{"--mount", bindOption(source, "/data", true)}, "/bin/sh", "-c", `
set -eu
[ ! -e /data/child/host-mount ]
[ "$(cat /data/child/underlying)" = original ]
if touch /data/child/denied 2>/dev/null; then exit 1; fi`)
	assertSourceFile(t, child, "host-mount", "private")
}

func TestBindMountCleanupOnTimeoutAndSupervisorLoss(t *testing.T) {
	require(t)
	source := t.TempDir()
	options := []string{"--mount", bindOption(source, "/data", false), "--timeout", "200ms"}
	code, _, stderr := run(t, options, "/bin/sh", "-c", "printf timeout > /data/keep; sleep 300")
	if code != 124 {
		t.Fatalf("timeout exit=%d stderr=%q", code, stderr)
	}
	assertSourceFile(t, source, "keep", "timeout")
	name := backgroundName(t)
	id := startBackground(t, name, []string{"--mount", bindOption(source, "/data", false)}, "/bin/sleep", "300")
	backgroundSuccess(t, "exec", name, "--", "/bin/sh", "-c", "printf recovery > /data/keep")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	output, err := exec.CommandContext(ctx, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", "casklet-"+id+".service").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("kill supervisor: %v: %s", err, output)
	}
	waitBackground(t, id, "failed")
	assertBackgroundUnitStopped(t, id)
	backgroundSuccess(t, "rm", id)
	assertSourceFile(t, source, "keep", "recovery")
	success(t, []string{"--mount", bindOption(source, "/data", true)}, "/bin/sh", "-c", "[ \"$(cat /data/keep)\" = recovery ]")
}

func TestBindMountPreservesSourceMountRestrictions(t *testing.T) {
	require(t)
	source := t.TempDir()
	if err := unix.Mount("tmpfs", source, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "size=1m"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(source, 0); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(filepath.Join(source, "script"), []byte("#!/bin/sh\necho must-not-run\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("", source, "", unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		t.Fatal(err)
	}
	// The default writable option must not relax an already read-only source.
	success(t, []string{"--mount", bindOption(source, "/data", false)}, "/bin/sh", "-c", `
set -eu
if /data/script 2>/dev/null; then exit 1; fi
if touch /data/new 2>/dev/null; then exit 1; fi
awk '$5 == "/data" { print $6 }' /proc/self/mountinfo | grep '^ro,.*noexec'`)
	if err := os.WriteFile(filepath.Join(source, "host-write"), []byte("denied"), 0600); !os.IsPermission(err) && !errors.Is(err, unix.EROFS) {
		t.Fatalf("source mount lost read-only restriction: %v", err)
	}
}
