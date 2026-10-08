//go:build linux

package machine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/mingo-liu/casklet/internal/template"
)

// EnsureBuiltinTemplate is a private guest installation operation. Only the
// fixed builtin source is repairable; user templates and retained roots are not.
func EnsureBuiltinTemplate(ctx context.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("builtin template installation requires guest root privileges")
	}
	return template.EnsureBuiltin(ctx, func(ctx context.Context, destination string) error {
		data, err := assets.ReadFile("assets/prepare-rootfs.sh")
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-s", "--", destination)
		cmd.Stdin = bytes.NewReader(data)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		// Use the installed package, independently of caller-controlled overrides.
		cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
		return cmd.Run()
	})
}
