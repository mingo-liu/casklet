//go:build linux

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func executionTemplate(t *testing.T) string {
	t.Helper()
	require(t)
	source := t.TempDir()
	if out, err := exec.Command("cp", "-a", template+"/.", source).CombinedOutput(); err != nil {
		t.Fatalf("copy execution template: %v: %s", err, out)
	}
	if err := os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	return source
}

func writeTemplateFile(t *testing.T, source, name, content string, mode os.FileMode) {
	t.Helper()
	file := filepath.Join(source, name)
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCommandEnvironment(t *testing.T) {
	out := success(t, []string{"--env", "EMPTY=", "--env", "VALUE=first", "--env", "VALUE=last=part", "--env", "HOME=/custom-home"}, "/bin/sh", "-c", `
set -eu
[ "${EMPTY+x}" = x ] && [ -z "$EMPTY" ]
[ "$VALUE" = last=part ]
[ "$HOME" = /custom-home ]
[ "$PATH" = /bin:/usr/bin ]
[ "$LANG" = C ]
[ -z "${CASKLET_HOST_SECRET+x}" ]
[ "$(env | grep -c '^VALUE=')" = 1 ]
echo environment-ok`)
	if out != "environment-ok\n" {
		t.Fatalf("unexpected environment output %q", out)
	}

	source := executionTemplate(t)
	writeTemplateFile(t, source, "custom-bin/only-here", "#!/bin/sh\nprintf 'configured-path:%s\\n' \"$VALUE\"\n", 0755)
	out = success(t, []string{"--rootfs", source, "--env", "PATH=/custom-bin", "--env", "VALUE=selected"}, "only-here")
	if out != "configured-path:selected\n" {
		t.Fatalf("configured PATH was not used for command lookup: %q", out)
	}
	code, out, stderr := run(t, []string{"--rootfs", source}, "only-here")
	if code != 125 || out != "" {
		t.Fatalf("command outside default PATH unexpectedly ran: exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}

func TestWorkingDirectory(t *testing.T) {
	if out := success(t, nil, "/bin/pwd"); out != "/\n" {
		t.Fatalf("default working directory = %q", out)
	}
	source := executionTemplate(t)
	writeTemplateFile(t, source, "workspace/relative-command", "#!/bin/sh\nset -eu\n[ \"$PWD\" = /workspace ]\nprintf changed > container-file\necho workdir-ok\n", 0755)
	out := success(t, []string{"--rootfs", source, "--workdir", "/workspace/./"}, "./relative-command")
	if out != "workdir-ok\n" {
		t.Fatalf("relative command did not run in the working directory: %q", out)
	}
	if _, err := os.Stat(filepath.Join(source, "workspace", "container-file")); !os.IsNotExist(err) {
		t.Fatalf("command modified the original template: %v", err)
	}
	code, out, stderr := run(t, []string{"--rootfs", source, "--workdir", "/does-not-exist"}, "/bin/echo", "must-not-run")
	if code != 125 || out != "" || !strings.Contains(stderr, "working directory") {
		t.Fatalf("missing workdir exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if out := success(t, []string{"--rootfs", source}, "/bin/sh", "-c", "[ ! -e /does-not-exist ] && echo missing"); out != "missing\n" {
		t.Fatalf("missing working directory was created: %q", out)
	}
}

const checkIdentity = `
set -eu
for status in /proc/self/status /proc/1/status; do
  awk -v uid="$1" -v gid="$2" '
    /^Uid:/ { for (i=2; i<=5; i++) if ($i != uid) exit 1; seen++ }
    /^Gid:/ { for (i=2; i<=5; i++) if ($i != gid) exit 1; seen++ }
    /^Groups:/ { if (NF != 1) exit 1; seen++ }
    /^Cap(Inh|Prm|Eff|Bnd|Amb):/ { if ($2 !~ /^0+$/) exit 1; seen++ }
    /^NoNewPrivs:/ { if ($2 != 1) exit 1; seen++ }
    END { if (seen != 9) exit 1 }
  ' "$status"
done
printf writable > /tmp/user-write
[ "$(cat /tmp/user-write)" = writable ]
if [ "$1" != 0 ]; then
  if cat /root-private >/dev/null 2>&1; then exit 1; fi
else
  [ "$(cat /root-private)" = root-private ]
fi
echo identity-ok`

func TestNumericUser(t *testing.T) {
	source := executionTemplate(t)
	writeTemplateFile(t, source, "root-private", "root-private", 0600)
	for _, test := range []struct {
		name, user, uid, gid string
	}{
		{"default-root", "", "0", "0"},
		{"implicit-group", "1000", "1000", "1000"},
		{"explicit-group", "1000:1234", "1000", "1234"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := []string{"--rootfs", source}
			if test.user != "" {
				options = append(options, "--user", test.user)
			}
			if out := success(t, options, "/bin/sh", "-c", checkIdentity, "sh", test.uid, test.gid); out != "identity-ok\n" {
				t.Fatalf("unexpected identity output %q", out)
			}
		})
	}
	if out := success(t, []string{"--user", "1000"}, "/bin/id", "-u"); out != "1000\n" {
		t.Fatalf("standard rootfs cannot run as numeric user: %q", out)
	}
}

func TestNonrootProcessLifecycle(t *testing.T) {
	t.Run("signal-trap", func(t *testing.T) {
		call := start(t, "", "run", "--rootfs", template, "--user", "1000", "--", "/bin/sh", "-c", "trap 'echo interrupted; exit 0' TERM; echo ready; while :; do sleep 1; done")
		pid := call.supervisor(t)
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := call.wait(t)
		if code != 0 || !strings.Contains(out, "interrupted") {
			t.Fatalf("nonroot signal exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		started := time.Now()
		code, out, stderr := run(t, []string{"--user", "1000", "--timeout", "300ms"}, "/bin/sh", "-c", "echo started; sleep 30")
		if code != 124 || !strings.Contains(out, "started") {
			t.Fatalf("nonroot timeout exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("nonroot timeout cleanup exceeded its bound: %v", elapsed)
		}
	})
	t.Run("stubborn-orphan", func(t *testing.T) {
		started := time.Now()
		code, out, stderr := run(t, []string{"--user", "1000", "--timeout", "500ms"}, "/bin/sh", "-c", `
set -eu
/bin/sh -c 'trap "" TERM; echo ready > /tmp/orphan-ready; while :; do sleep 30; done' &
while [ ! -e /tmp/orphan-ready ]; do sleep 0.01; done
echo main-exited
exit 7`)
		if code != 7 || !strings.Contains(out, "main-exited") {
			t.Fatalf("nonroot descendant cleanup exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		if elapsed := time.Since(started); elapsed > 8*time.Second {
			t.Fatalf("nonroot descendant cleanup exceeded its bound: %v", elapsed)
		}
	})
}

func TestReadOnlyRootFilesystem(t *testing.T) {
	source := executionTemplate(t)
	writeTemplateFile(t, source, "public/existing", "template-original", 0666)
	if err := os.Chmod(filepath.Join(source, "public"), 0777); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"0", "1000"} {
		t.Run("user-"+user, func(t *testing.T) {
			out := success(t, []string{"--rootfs", source, "--read-only", "--user", user}, "/bin/sh", "-c", `
set -eu
awk '$5 == "/" {print $6}' /proc/self/mountinfo | grep '^ro,'
for path in /public/new-file /public/existing; do
  if (printf changed > "$path") 2>/tmp/root-write-error; then exit 1; fi
  grep -q 'Read-only file system' /tmp/root-write-error
done
[ "$(cat /public/existing)" = template-original ]
printf writable > /tmp/scratch
[ "$(cat /tmp/scratch)" = writable ]
[ "$(stat -c %a /tmp)" = 1777 ]
printf usable > /dev/null
[ "$(dd if=/dev/zero bs=1 count=4 2>/dev/null | wc -c)" = 4 ]
[ -c /dev/random ] && [ -c /dev/urandom ]
echo read-only-ok`)
			if !strings.Contains(out, "read-only-ok\n") {
				t.Fatalf("unexpected read-only output %q", out)
			}
		})
	}
	if out := success(t, []string{"--rootfs", source}, "/bin/sh", "-c", "printf writable > /public/new-file; printf changed > /public/existing; echo writable-ok"); out != "writable-ok\n" {
		t.Fatalf("default root filesystem is not writable: %q", out)
	}
	if _, err := os.Stat(filepath.Join(source, "public", "new-file")); !os.IsNotExist(err) {
		t.Fatalf("container created a file in its source template: %v", err)
	}
	original, err := os.ReadFile(filepath.Join(source, "public", "existing"))
	if err != nil || string(original) != "template-original" {
		t.Fatalf("template content changed: %q, %v", original, err)
	}
}

func TestCombinedExecutionOptions(t *testing.T) {
	source := executionTemplate(t)
	writeTemplateFile(t, source, "workspace/check", "#!/bin/sh\nset -eu\n[ \"$(id -u)\" = 1000 ]\n[ \"$PWD\" = /workspace ]\n[ \"$VALUE\" = combined ]\nprintf scratch > /tmp/combined\necho combined-ok\n", 0755)
	options := []string{"--rootfs", source, "--user", "1000:1234", "--read-only", "--workdir", "/workspace", "--env", "VALUE=combined", "--env", "PATH=/workspace:/bin"}
	if out := success(t, options, "check"); out != "combined-ok\n" {
		t.Fatalf("combined execution options output %q", out)
	}
}
