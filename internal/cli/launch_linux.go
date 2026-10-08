//go:build linux

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/mingo-liu/casklet/internal/cgroup"
	"golang.org/x/sys/unix"
)

const scopeEnvironment = "CASKLET_SCOPE_LAUNCHED"

// prepareLaunch replaces the CLI process so terminal descriptors, signals, and
// workload exit codes pass through the privilege and delegation setup.
func prepareLaunch(args []string, request Request) error {
	if request.Action == "help" {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve runtime executable: %w", err)
	}
	if request.Config.Rootless && os.Geteuid() == 0 {
		return errors.New("--rootless requires an unprivileged host user")
	}
	if os.Geteuid() != 0 && !request.Config.Rootless {
		if _, err := exec.LookPath("/usr/bin/sudo"); err != nil {
			return errors.New("root privileges are required; install sudo or run casklet as root")
		}
		command := append([]string{"sudo", "--", executable}, args...)
		return unix.Exec("/usr/bin/sudo", command, os.Environ())
	}
	if request.Action != "doctor" && (request.Action != "run" || request.Detach) {
		return nil
	}
	delegationErr := cgroup.Check()
	if delegationErr == nil {
		return nil
	}
	// A failed scope must report its prerequisite error rather than recurse.
	if os.Getenv(scopeEnvironment) == "1" {
		return fmt.Errorf("automatic cgroup delegation failed: %w", delegationErr)
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("foreground containers require a running systemd system instance")
	}
	if _, err := exec.LookPath("/usr/bin/systemd-run"); err != nil {
		return errors.New("foreground containers require /usr/bin/systemd-run")
	}
	if err := os.Setenv(scopeEnvironment, "1"); err != nil {
		return err
	}
	scopeArgs := []string{"systemd-run"}
	if request.Config.Rootless {
		scopeArgs = append(scopeArgs, "--user")
	}
	command := append(append(scopeArgs, "--scope", "--quiet", "--property=Delegate=memory pids cpu", "--", executable), args...)
	return unix.Exec("/usr/bin/systemd-run", command, os.Environ())
}
