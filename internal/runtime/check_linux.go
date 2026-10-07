//go:build linux

package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/mingo-liu/mini-docker/internal/cgroup"
	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/rootfs"
	"golang.org/x/sys/unix"
)

func Check(template string) error {
	return checkConfig(config.Config{RootFS: template})
}

func checkConfig(cfg config.Config) error {
	if err := validateExecutionMode(cfg); err != nil {
		return err
	}
	template := cfg.RootFS
	if _, err := rootfs.Validate(template); err != nil {
		return err
	}
	if err := cgroup.Check(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), startupLimit)
	defer cancel()
	probeBinary, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer probeBinary.Close()
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", "__probe")
	cmd.ExtraFiles = []*os.File{probeBinary}
	cmd.Env = baseEnvironment
	cmd.SysProcAttr = namespaceAttributes(cfg)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("probe namespace capabilities: %w: %s", err, output)
	}
	return nil
}

// Probe runs only in a temporary namespace child created by Check.
func Probe() int {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "mini-docker: namespace probe requires container PID 1")
		return 125
	}
	if err := enableLoopback(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 125
	}
	if err := unix.Sethostname([]byte("mini-probe")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 125
	}
	if err := reducePrivileges(nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 125
	}
	if err := installSeccomp("default"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 125
	}
	return 0
}
