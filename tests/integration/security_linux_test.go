//go:build linux

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSecuritySeccomp(t *testing.T) {
	for _, profile := range []string{"default", "unconfined"} {
		t.Run(profile, func(t *testing.T) {
			command := "security"
			if profile == "unconfined" {
				command += "-unconfined"
			}
			if out := success(t, []string{"--seccomp", profile}, "/bin/integration-helper", command); out != "security-ok\n" {
				t.Fatalf("probe = %q", out)
			}
			name := backgroundName(t)
			backgroundSuccess(t, "run", "-d", "--name", name, "--rootfs", template, "--seccomp", profile, "--", "/bin/sleep", "30")
			if out := backgroundSuccess(t, "exec", name, "--", "/bin/integration-helper", command); out != "security-ok\n" {
				t.Fatalf("exec probe = %q", out)
			}
			inspection := backgroundSuccess(t, "inspect", name)
			if !strings.Contains(inspection, `"seccomp": "`+profile+`"`) {
				t.Fatalf("inspection = %s", inspection)
			}
			backgroundSuccess(t, "restart", "--timeout", "0s", name)
			backgroundSuccess(t, "exec", name, "--", "/bin/integration-helper", command)
		})
	}
}

func mappedOptions() []string {
	return []string{"--userns", "--uid-map", "0:200000:1000", "--uid-map", "1000:400000:1000", "--gid-map", "0:300000:2000"}
}

func TestSecurityUserNamespaces(t *testing.T) {
	require(t)
	source := executionTemplate(t)
	writeTemplateFile(t, source, "root-private", "root-private", 0600)
	for _, identity := range []string{"0:0", "1000:1234"} {
		t.Run(identity, func(t *testing.T) {
			options := append(mappedOptions(), "--rootfs", source, "--user", identity)
			uid, gid, _ := strings.Cut(identity, ":")
			if out := success(t, options, "/bin/sh", "-c", checkIdentity, "sh", uid, gid); out != "identity-ok\n" {
				t.Fatalf("identity = %q", out)
			}
			out := success(t, options, "/bin/sh", "-c", "cat /proc/self/uid_map; cat /proc/self/gid_map; /bin/integration-helper security")
			for _, expected := range []string{"200000", "400000", "300000", "security-ok"} {
				if !strings.Contains(out, expected) {
					t.Fatalf("missing %s in %q", expected, out)
				}
			}
		})
	}
	data, err := os.MkdirTemp("/tmp", "casklet-security-bind-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	if err := os.Chown(data, 200000, 300000); err != nil {
		t.Fatal(err)
	}
	options := append(mappedOptions(), "--read-only", "--mount", "type=bind,source="+data+",target=/data")
	success(t, options, "/bin/sh", "-c", "echo mapped > /data/result; echo temp > /tmp/result; test ! -e /result; /bin/integration-helper security")
	info, err := os.Stat(filepath.Join(data, "result"))
	if err != nil {
		t.Fatal(err)
	}
	// stat uses the host identity, independently of /proc's namespace view.
	if output, err := exec.Command("stat", "-c", "%u:%g", filepath.Join(data, "result")).Output(); err != nil || strings.TrimSpace(string(output)) != "200000:300000" {
		t.Fatalf("ownership of %s: %s, %v", info.Name(), output, err)
	}
	for _, mode := range []string{"memory", "pids", "cpu", "tty", "timeout", "startup"} {
		t.Run(mode, func(t *testing.T) {
			options := mappedOptions()
			command := []string{"/bin/integration-helper", mode}
			expected := 0
			switch mode {
			case "memory":
				options = append(options, "--memory", "32m")
				expected = 137
			case "pids":
				options = append(options, "--pids-limit", "32")
			case "cpu":
				options = append(options, "--cpus", "0.1")
			case "tty":
				options = append(options, "--tty")
				command = []string{"/bin/integration-helper", "security"}
			case "timeout":
				options = append(options, "--timeout", "100ms", "--stop-timeout", "0s")
				command = []string{"/bin/sleep", "30"}
				expected = 124
			case "startup":
				command = []string{"/missing-command"}
				expected = 125
			}
			code, out, stderr := run(t, options, command...)
			if mode == "cpu" && code == 0 {
				checkCPUQuota(t, out)
			}
			if code != expected {
				t.Fatalf("exit=%d expected=%d out=%s stderr=%s", code, expected, out, stderr)
			}
		})
	}
	t.Run("supervisor-recovery", func(t *testing.T) {
		args := append([]string{"run", "--rootfs", template, "--stop-timeout", "0s"}, mappedOptions()...)
		args = append(args, "--", "/bin/sh", "-c", "echo ready; sleep 30")
		call := start(t, "", args...)
		pid := call.supervisor(t)
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		code, _, _ := call.wait(t)
		if code == 0 {
			t.Fatal("killed mapped supervisor returned success")
		}
		success(t, mappedOptions(), "/bin/echo", "recovered")
	})

	entries, err := os.ReadDir("/tmp/casklet-userns")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "run-") {
			t.Fatalf("mapped run leaked: %s", entry.Name())
		}
	}
}

func TestSecurityRootless(t *testing.T) {
	require(t)
	uid := os.Getenv("CASKLET_ROOTLESS_UID")
	if uid == "" {
		uid = os.Getenv("SUDO_UID")
	}
	if uid == "" || uid == "0" {
		t.Fatal("rootless integration requires SUDO_UID or CASKLET_ROOTLESS_UID for a logged-in unprivileged user")
	}
	account, err := user.LookupId(uid)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.MkdirTemp("/tmp", "casklet-rootless-template-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(source)
	for _, args := range [][]string{{"cp", "-a", template + "/.", source}, {"chown", "-R", uid + ":" + account.Gid, source}} {
		if output, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("prepare rootless template: %v: %s", err, output)
		}
	}
	launch := func(options []string, command ...string) (int, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		runtimeDir := "/run/user/" + uid
		args := []string{"-u", account.Username, "--", "env", "XDG_RUNTIME_DIR=" + runtimeDir, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus"}
		// Ubuntu's optional userns restriction requires the scoped development profile.
		policy, _ := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
		if strings.TrimSpace(string(policy)) == "1" {
			args = append(args, "aa-exec", "-p", "casklet-rootless", "--")
		}
		args = append(args, binary, "run", "--rootless", "--rootfs", source)
		args = append(args, options...)
		args = append(args, "--")
		args = append(args, command...)
		// A real rootless invocation inherits caller-owned output descriptors.
		// runuser's root-owned capture pipe cannot be reopened by a TTY bridge.
		capture, err := os.CreateTemp("/tmp", "casklet-rootless-output-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.Remove(capture.Name())
		defer capture.Close()
		owner, _ := strconv.Atoi(uid)
		group, _ := strconv.Atoi(account.Gid)
		if err := capture.Chown(owner, group); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, "runuser", args...)
		cmd.Stdout, cmd.Stderr = capture, capture
		err = cmd.Run()
		output, readErr := os.ReadFile(capture.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("rootless command timed out: %s", output)
		}
		return code, string(output)
	}
	for _, test := range []struct {
		name             string
		options, command []string
		code             int
		text             string
	}{
		{"identity", nil, []string{"/bin/sh", "-c", "id -u; cat /proc/self/uid_map; cat /proc/self/gid_map; /bin/integration-helper security"}, 0, "security-ok"},
		{"read-only", []string{"--read-only"}, []string{"/bin/sh", "-c", "echo tmp > /tmp/probe; ! touch /root-probe; cat /tmp/probe"}, 0, "tmp"},
		{"tty", []string{"--tty"}, []string{"/bin/integration-helper", "security"}, 0, "security-ok"},
		{"pids", []string{"--pids-limit", "32"}, []string{"/bin/integration-helper", "pids"}, 0, "process creation denied"},
		{"memory", []string{"--memory", "32m"}, []string{"/bin/integration-helper", "memory"}, 137, "OOM"},
		{"cpu", []string{"--cpus", "0.1"}, []string{"/bin/integration-helper", "cpu"}, 0, "cpu_us="},
		{"timeout", []string{"--timeout", "100ms", "--stop-timeout", "0s"}, []string{"/bin/sleep", "30"}, 124, ""},
		{"startup", nil, []string{"/missing-command"}, 125, ""},
		{"no-fallback", []string{"--network", "bridge"}, []string{"/bin/echo", "must-not-run"}, 125, "rootless"},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, out := launch(test.options, test.command...)
			if code != test.code || !strings.Contains(out, test.text) {
				t.Fatalf("exit=%d expected=%d output=%q", code, test.code, out)
			}
			if test.name == "identity" {
				hostUID, _ := strconv.Atoi(uid)
				fields := strings.Fields(out)
				if len(fields) < 7 || fields[0] != "0" || fields[2] != fmt.Sprint(hostUID) || fields[5] != account.Gid {
					t.Fatalf("mapping output = %q", out)
				}
			}
			if test.name == "cpu" {
				checkCPUQuota(t, out)
			}
		})
	}
	data, err := os.MkdirTemp("/tmp", "casklet-rootless-bind-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	owner, _ := strconv.Atoi(uid)
	group, _ := strconv.Atoi(account.Gid)
	if err := os.Chown(data, owner, group); err != nil {
		t.Fatal(err)
	}
	for _, readonly := range []bool{false, true} {
		mount := "type=bind,source=" + data + ",target=/data"
		command := "echo persistent > /data/result"
		if readonly {
			mount += ",readonly"
			command = "test -f /data/result; ! echo forbidden > /data/result"
		}
		code, out := launch([]string{"--read-only", "--mount", mount}, "/bin/sh", "-c", command)
		if code != 0 {
			t.Fatalf("rootless bind readonly=%v: exit=%d output=%q", readonly, code, out)
		}
	}
	if output, err := os.ReadFile(filepath.Join(data, "result")); err != nil || string(output) != "persistent\n" {
		t.Fatalf("bind data was not retained: %s, %v", output, err)
	}
	root := "/run/user/" + uid + "/casklet/runs"
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "run-") {
			t.Fatalf("rootless run leaked: %s", entry.Name())
		}
	}
}

func checkCPUQuota(t *testing.T, output string) {
	t.Helper()
	var elapsed, cpu int64
	for _, field := range strings.Fields(output) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "elapsed_us":
			elapsed, _ = strconv.ParseInt(value, 10, 64)
		case "cpu_us":
			cpu, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if elapsed < 3000000 || cpu <= 0 || float64(cpu)/float64(elapsed) > 0.25 {
		t.Fatalf("CPU quota was not enforced: %s", output)
	}
}
