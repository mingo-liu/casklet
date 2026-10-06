//go:build linux

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func startExecTerminal(t *testing.T, options []string, reference string, command ...string) *terminalInvocation {
	t.Helper()
	args := append([]string{"exec", "-it"}, options...)
	args = append(args, reference, "--")
	args = append(args, command...)
	return startTerminalArguments(t, args)
}

func signalExecTerminal(t *testing.T, call *terminalInvocation, sig syscall.Signal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	control, err := exec.CommandContext(ctx, "systemctl", "show", call.unit, "--property=ControlGroup", "--value").Output()
	if err != nil {
		t.Fatal(err)
	}
	relative := strings.TrimSpace(string(control))
	if relative == "" || relative == "/" || !strings.HasPrefix(relative, "/") || filepath.Clean(relative) != relative {
		t.Fatalf("invalid exec client scope cgroup: %q", relative)
	}
	root := "/sys/fs/cgroup" + relative
	for _, file := range []string{filepath.Join(root, "manager", "cgroup.procs"), filepath.Join(root, "cgroup.procs")} {
		processes, _ := os.ReadFile(file)
		for _, value := range strings.Fields(string(processes)) {
			args, _ := os.ReadFile("/proc/" + value + "/cmdline")
			executable, _ := os.Readlink("/proc/" + value + "/exe")
			command := strings.Split(string(args), "\x00")
			if executable == binary && len(command) > 1 && command[1] == "exec" {
				pid, err := strconv.Atoi(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, sig); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
	t.Fatal("exec client is missing from its terminal scope")
}

func execTerminalCgroup(t *testing.T, call *terminalInvocation, marker string) string {
	t.Helper()
	call.outputContains(t, marker)
	out, err := call.captured()
	if err != nil {
		t.Fatal(err)
	}
	return execCgroup(t, strings.ReplaceAll(out, "\r\n", "\n"))
}

func assertExecTerminalReleased(t *testing.T, reference string) {
	t.Helper()
	if out := execSuccess(t, nil, reference, "/bin/sh", "-c", `[ "$(ls /dev/pts)" = ptmx ] && echo terminals-released`); out != "terminals-released\n" {
		t.Fatalf("exec retained a private PTY: %q", out)
	}
}

func TestExecTerminalShellInteractionAndResize(t *testing.T) {
	name, id := execContainer(t, nil)
	call := startExecTerminal(t, nil, name, "/bin/sh", "-c", `
set -eu
test -t 0 && test -t 1 && test -t 2
[ "$TERM" = xterm ]
cat /proc/self/cgroup
printf 'exec-terminal-ready\n' > /dev/tty
exec /bin/sh`)
	group := execTerminalCgroup(t, call, "exec-terminal-ready\r\n")
	active, err := unix.IoctlGetTermios(int(call.pty.slave.Fd()), unix.TCGETS)
	if err != nil || active.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != 0 {
		t.Fatalf("exec host terminal did not enter raw mode: %+v error=%v", active, err)
	}
	call.write(t, "printf 'EXEC-INITIAL-SIZE:'; stty size\n")
	call.outputContains(t, "EXEC-INITIAL-SIZE:24 80\r\n")
	if err := unix.IoctlSetWinsize(int(call.pty.slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 43, Col: 109}); err != nil {
		t.Fatal(err)
	}
	signalExecTerminal(t, call, syscall.SIGWINCH)
	call.write(t, `i=0; while [ "$(stty size)" != "43 109" ] && [ "$i" -lt 100 ]; do i=$((i+1)); sleep .02; done; printf 'EXEC-UPDATED-SIZE:'; stty size`+"\n")
	call.outputContains(t, "EXEC-UPDATED-SIZE:43 109\r\n")
	call.write(t, "/bin/sh -c 'echo exec-sleep-ready; exec sleep 30'\n")
	call.outputContains(t, "exec-sleep-ready\r\n")
	call.write(t, "\x03")
	call.write(t, "printf 'EXEC-AFTER-INT\\n'\n")
	call.outputContains(t, "EXEC-AFTER-INT\r\n")
	waitBackground(t, id, "running")
	call.write(t, "exit 7\n")
	code, out, stderr := call.wait(t)
	if code != 7 || stderr != "" || strings.Contains(out, "job control turned off") || strings.Contains(out, "can't access tty") {
		t.Fatalf("interactive exec shell exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertExecCgroupRemoved(t, group)
	assertExecTerminalReleased(t, id)
	if logs := backgroundSuccess(t, "logs", name); strings.Contains(logs, "exec-terminal-ready") || strings.Contains(logs, "EXEC-AFTER-INT") {
		t.Fatalf("terminal exec output entered background logs: %q", logs)
	}
}

func TestExecTerminalInteractiveEOF(t *testing.T) {
	name, id := execContainer(t, nil)
	call := startExecTerminal(t, nil, name, "/bin/sh", "-c", "echo exec-cat-ready; exec cat")
	call.outputContains(t, "exec-cat-ready\r\n")
	call.write(t, "exec-interactive-input\n\x04")
	code, out, stderr := call.wait(t)
	if code != 0 || strings.Count(out, "exec-interactive-input\r\n") != 2 || stderr != "" {
		t.Fatalf("terminal exec EOF exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	waitBackground(t, id, "running")
	assertExecTerminalReleased(t, id)
}

func TestExecTerminalInheritsNonrootReadOnlyAndPrivateDevpts(t *testing.T) {
	require(t)
	_ = openTerminalPTY(t)
	marker := openTerminalPTY(t)
	name, id := execContainer(t, []string{"--user", "1000:1234", "--read-only"})
	call := startExecTerminal(t, []string{"--env", "TERM=vt100"}, name, "/bin/sh", "-c", `
set -eu
[ "$(id -u)" = 1000 ] && [ "$(id -g)" = 1234 ]
[ "$TERM" = vt100 ]
test -t 0 && test -t 1 && test -t 2
[ -c /dev/pts/ptmx ] && [ -c /dev/ptmx ] && [ -c /dev/tty ]
[ ! -e "/dev/pts/$1" ]
awk '$2 == "/dev/pts" && $3 == "devpts" {print $4}' /proc/mounts | grep -q 'max=64'
for descriptor in /proc/$$/fd/*; do
  case "${descriptor##*/}" in 0|1|2) continue ;; esac
  [ ! -L "$descriptor" ]
done
awk '/^Groups:/ { if (NF != 1) exit 1; seen++ }
     /^Cap(Inh|Prm|Eff|Bnd|Amb):/ { if ($2 !~ /^0+$/) exit 1; seen++ }
     /^NoNewPrivs:/ { if ($2 != 1) exit 1; seen++ }
     END { if (seen != 7) exit 1 }' /proc/self/status
if (printf forbidden > /exec-root-write) 2>/dev/null; then exit 1; fi
printf usable > /tmp/exec-tty-scratch
printf 'exec-private-ready\n' > /dev/tty
read value < /dev/tty
[ "$value" = private-exec-input ]
echo exec-private-ok`, "sh", strconv.Itoa(int(marker.number)))
	call.outputContains(t, "exec-private-ready\r\n")
	call.write(t, "private-exec-input\n")
	code, out, stderr := call.wait(t)
	if code != 0 || !strings.Contains(out, "exec-private-ok\r\n") || stderr != "" {
		t.Fatalf("nonroot read-only terminal exec exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	waitBackground(t, id, "running")
	assertExecTerminalReleased(t, id)
}

func TestExecTerminalInputModesAndMergedOutput(t *testing.T) {
	name, id := execContainer(t, []string{"--env", "TERM=container-term"})
	t.Run("tty-without-input", func(t *testing.T) {
		code, out, stderr := startExec(t, "must-not-forward\n", []string{"-t"}, name, "/bin/sh", "-c", `
set -eu
test -t 0 && test -t 1 && test -t 2
[ "$TERM" = container-term ]
[ "$(stty size)" = "24 80" ]
if read value; then exit 1; fi
printf 'exec-tty-stdout\n'
printf 'exec-tty-stderr\n' >&2`).wait(t)
		if code != 0 || out != "exec-tty-stdout\r\nexec-tty-stderr\r\n" || stderr != "" {
			t.Fatalf("output-only exec TTY exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertExecTerminalReleased(t, id)
	})
	t.Run("interactive-requires-terminal", func(t *testing.T) {
		code, out, stderr := startExec(t, "must-not-forward\n", []string{"-it"}, id, "/bin/cat").wait(t)
		if code != 125 || out != "" || !strings.Contains(strings.ToLower(stderr), "terminal") {
			t.Fatalf("exec -it accepted pipe input: exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertExecTerminalReleased(t, id)
	})
	waitBackground(t, id, "running")
}

func TestExecTerminalConcurrentSessionsRemainIndependent(t *testing.T) {
	name, id := execContainer(t, []string{"--pids-limit", "128"})
	command := `printf 'TTY:%s\n' "$(tty)"; cat /proc/self/cgroup; echo "$1-ready"; read value; [ "$value" = "$1-input" ]; echo "$1-complete"`
	first := startExecTerminal(t, nil, name, "/bin/sh", "-c", command, "sh", "first")
	firstGroup := execTerminalCgroup(t, first, "first-ready\r\n")
	second := startExecTerminal(t, nil, id, "/bin/sh", "-c", command, "sh", "second")
	secondGroup := execTerminalCgroup(t, second, "second-ready\r\n")
	firstOut, _ := first.captured()
	secondOut, _ := second.captured()
	firstTTY, secondTTY := "", ""
	for _, line := range strings.Split(firstOut, "\r\n") {
		if value, ok := strings.CutPrefix(line, "TTY:"); ok {
			firstTTY = value
		}
	}
	for _, line := range strings.Split(secondOut, "\r\n") {
		if value, ok := strings.CutPrefix(line, "TTY:"); ok {
			secondTTY = value
		}
	}
	if firstTTY == "" || secondTTY == "" || firstTTY == secondTTY || firstGroup == secondGroup {
		t.Fatalf("sessions shared PTY/cgroup: first=%q %s second=%q %s", firstTTY, firstGroup, secondTTY, secondGroup)
	}
	first.write(t, "first-input\n")
	if code, out, stderr := first.wait(t); code != 0 || !strings.Contains(out, "first-complete\r\n") || strings.Contains(out, "second-input") {
		t.Fatalf("first exec session exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	second.write(t, "second-input\n")
	if code, out, stderr := second.wait(t); code != 0 || !strings.Contains(out, "second-complete\r\n") || strings.Contains(out, "first-input") {
		t.Fatalf("second exec session exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	assertExecCgroupRemoved(t, firstGroup)
	assertExecCgroupRemoved(t, secondGroup)
	waitBackground(t, id, "running")
	assertExecTerminalReleased(t, id)
}

func TestExecTerminalExternalSignalTargetsForegroundJob(t *testing.T) {
	name, id := execContainer(t, nil)
	const prompt = "exec-external-prompt> "
	for _, test := range []struct {
		name   string
		signal syscall.Signal
	}{
		{"INT", syscall.SIGINT},
		{"TERM", syscall.SIGTERM},
		{"HUP", syscall.SIGHUP},
	} {
		t.Run(test.name, func(t *testing.T) {
			call := startExecTerminal(t, []string{"--env", "PS1=" + prompt}, name, "/bin/sh", "-c", "echo exec-external-shell-ready; exec /bin/sh")
			call.outputContains(t, "exec-external-shell-ready\r\n")
			call.outputContains(t, prompt)
			job := `/bin/sh -c 'echo exec-external-job-ready; exec sleep 30'`
			handled := "exec-foreground-" + test.name + "-handled"
			if test.signal != syscall.SIGINT {
				// Readiness comes from the child after the interactive parent has
				// assigned this separate job its foreground process group.
				job = fmt.Sprintf(`/bin/sh -c 'trap "echo %s; exit 43" %s; echo exec-external-job-ready; while :; do sleep .1; done'`, handled, test.name)
			}
			call.write(t, job+"\n")
			call.outputContains(t, "exec-external-job-ready\r\n")
			before, _ := call.captured()
			prompts := strings.Count(before, prompt)
			started := time.Now()
			signalExecTerminal(t, call, test.signal)
			deadline := started.Add(3 * time.Second)
			for {
				out, err := call.captured()
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(out, prompt) > prompts {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("exec %s did not interrupt foreground job: %q", test.name, out)
				}
				time.Sleep(10 * time.Millisecond)
			}
			continued := "EXEC-EXTERNAL-AFTER-" + test.name
			call.write(t, "echo "+continued+"; exit 41\n")
			code, out, stderr := call.wait(t)
			if code != 41 || !strings.Contains(out, continued+"\r\n") || (test.signal != syscall.SIGINT && !strings.Contains(out, handled+"\r\n")) {
				t.Fatalf("exec external %s exit=%d stdout=%q stderr=%q", test.name, code, out, stderr)
			}
			if elapsed := time.Since(started); elapsed > 3*time.Second {
				t.Fatalf("exec %s waited for forced shutdown: %s", test.name, elapsed)
			}
			waitBackground(t, id, "running")
			assertExecTerminalReleased(t, id)
		})
	}
}

func TestExecTerminalRestorationAndFailureCleanup(t *testing.T) {
	name, id := execContainer(t, nil)
	t.Run("startup-failure", func(t *testing.T) {
		call := startExecTerminal(t, nil, name, "/bin/does-not-exist")
		if code, out, stderr := call.wait(t); code != 125 || stderr == "" {
			t.Fatalf("exec terminal startup failure exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertExecTerminalReleased(t, id)
	})
	t.Run("timeout", func(t *testing.T) {
		call := startExecTerminal(t, []string{"--timeout", "300ms"}, id, "/bin/sh", "-c", "cat /proc/self/cgroup; echo exec-timeout-ready; sleep 30")
		group := execTerminalCgroup(t, call, "exec-timeout-ready\r\n")
		if code, out, stderr := call.wait(t); code != 124 {
			t.Fatalf("exec terminal timeout exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertExecCgroupRemoved(t, group)
		assertExecTerminalReleased(t, id)
	})
	t.Run("signal", func(t *testing.T) {
		call := startExecTerminal(t, nil, name, "/bin/sh", "-c", "trap 'echo exec-stopped; exit 23' TERM; cat /proc/self/cgroup; echo exec-signal-ready; while :; do sleep .1; done")
		group := execTerminalCgroup(t, call, "exec-signal-ready\r\n")
		signalExecTerminal(t, call, syscall.SIGTERM)
		if code, out, stderr := call.wait(t); code != 23 || !strings.Contains(out, "exec-stopped\r\n") {
			t.Fatalf("exec terminal signal exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertExecCgroupRemoved(t, group)
		assertExecTerminalReleased(t, id)
	})
	t.Run("client-interrupt", func(t *testing.T) {
		call := startExecTerminal(t, nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo exec-client-interrupt-ready; exec sleep 30")
		group := execTerminalCgroup(t, call, "exec-client-interrupt-ready\r\n")
		signalExecTerminal(t, call, syscall.SIGINT)
		if code, out, stderr := call.wait(t); code != 130 {
			t.Fatalf("exec client interruption exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertExecCgroupRemoved(t, group)
		assertExecTerminalReleased(t, id)
	})
	t.Run("descendant", func(t *testing.T) {
		call := startExecTerminal(t, nil, id, "/bin/sh", "-c", `
setsid /bin/sh -c 'trap "" TERM; echo ready > /tmp/exec-tty-orphan-ready; sleep 30' &
while [ ! -e /tmp/exec-tty-orphan-ready ]; do sleep .01; done
cat /proc/self/cgroup
echo exec-descendant-ready
exit 9`)
		group := execTerminalCgroup(t, call, "exec-descendant-ready\r\n")
		if code, out, stderr := call.wait(t); code != 9 {
			t.Fatalf("exec terminal descendant exit=%d stdout=%q stderr=%q", code, out, stderr)
		}
		assertExecCgroupRemoved(t, group)
		assertExecTerminalReleased(t, id)
	})
	waitBackground(t, id, "running")
}

func TestExecTerminalContainerStopRestoresHost(t *testing.T) {
	name, id := execContainer(t, nil)
	call := startExecTerminal(t, nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo exec-stop-ready; sleep 30")
	group := execTerminalCgroup(t, call, "exec-stop-ready\r\n")
	backgroundSuccess(t, "stop", id)
	if code, out, stderr := call.wait(t); code == 0 {
		t.Fatalf("stopped container retained a successful ongoing exec: stdout=%q stderr=%q", out, stderr)
	}
	assertExecCgroupRemoved(t, group)
	waitBackground(t, id, "exited")
	assertBackgroundUnitStopped(t, id)
}

func TestExecTerminalSupervisorLossRestoresHost(t *testing.T) {
	name, id := execContainer(t, nil)
	call := startExecTerminal(t, nil, name, "/bin/sh", "-c", "cat /proc/self/cgroup; echo exec-terminal-loss-ready; sleep 30")
	group := execTerminalCgroup(t, call, "exec-terminal-loss-ready\r\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	output, err := exec.CommandContext(ctx, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", "mini-docker-"+id+".service").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("kill terminal exec supervisor: %v: %s", err, output)
	}
	failed := waitBackground(t, id, "failed")
	if failed.ExitCode != nil || failed.Error == "" {
		t.Fatalf("supervisor loss guessed command exit status: %+v", failed)
	}
	if code, out, stderr := call.wait(t); code == 0 {
		t.Fatalf("supervisor loss produced successful terminal exec exit: stdout=%q stderr=%q", out, stderr)
	}
	assertExecCgroupRemoved(t, group)
	assertBackgroundUnitStopped(t, id)
}
