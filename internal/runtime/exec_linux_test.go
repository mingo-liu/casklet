//go:build linux

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"golang.org/x/sys/unix"
)

func TestExecConfigurationIsImmutableAndOffsetIndependent(t *testing.T) {
	file, err := sealExecConfig(config.Config{Command: []string{"/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString("changed"); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("write sealed configuration: %v", err)
	}
	if err := file.Truncate(0); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("truncate sealed configuration: %v", err)
	}
	if os.Geteuid() != 0 {
		if _, err := readExecConfig(int(file.Fd())); err == nil {
			t.Fatal("non-root configuration was accepted")
		}
		return
	}
	for i := 0; i < 2; i++ {
		cfg, err := readExecConfig(int(file.Fd()))
		if err != nil || len(cfg.Command) != 1 || cfg.Command[0] != "/bin/true" {
			t.Fatalf("read configuration %d: %#v, %v", i, cfg, err)
		}
	}
}

func TestExecConfigurationRejectsOversizedPayload(t *testing.T) {
	_, err := sealExecConfig(config.Config{Command: []string{"/bin/echo", strings.Repeat("x", execConfigLimit)}})
	if err == nil {
		t.Fatal("oversized configuration was accepted")
	}
}

func TestExecConfigurationRejectsUnsealedAndMalformedPayloads(t *testing.T) {
	for _, test := range []struct {
		name, data string
		sealed     bool
	}{
		{"unsealed", `{"command":["/bin/true"]}`, false},
		{"unknown field", `{"command":["/bin/true"],"unexpected":true}`, true},
		{"trailing data", `{"command":["/bin/true"]} {}`, true},
		{"missing command", `{}`, true},
		{"tty", `{"command":["/bin/true"],"tty":true}`, true},
		{"negative timeout", `{"command":["/bin/true"],"timeout":-1}`, true},
		{"malformed", `{`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fd, err := unix.MemfdCreate("exec-test", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
			if err != nil {
				t.Fatal(err)
			}
			file := os.NewFile(uintptr(fd), "exec-test")
			defer file.Close()
			if _, err := file.WriteString(test.data); err != nil {
				t.Fatal(err)
			}
			if test.sealed {
				if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, execConfigSeals); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readExecConfig(fd); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestExecuteRequiresLiveResources(t *testing.T) {
	code, err := ExecuteInContainer(context.Background(), ExecResources{}, config.Exec{Command: []string{"/bin/true"}}, nil, nil, nil, nil)
	if code != 125 || err == nil {
		t.Fatalf("missing execution resources: code=%d, error=%v", code, err)
	}
}

func TestExecExitStatus(t *testing.T) {
	for _, test := range []struct {
		command string
		code    int
	}{
		{"exit 0", 0},
		{"exit 7", 7},
		{"kill -TERM $$", 143},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, "/bin/sh", "-c", test.command)
		_ = command.Run()
		cancel()
		if command.ProcessState == nil || execStatus(command.ProcessState) != test.code {
			t.Fatalf("%q: expected %d, got %v", test.command, test.code, command.ProcessState)
		}
	}
}
