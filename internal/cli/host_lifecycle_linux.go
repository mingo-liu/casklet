//go:build linux

package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/container"
	"github.com/mingo-liu/mini-docker/internal/remote"
)

// ExecuteHostLifecycle is a private transport mode. The guest owns locking,
// stopping and generation changes; the Mac authorizes its published ports.
func ExecuteHostLifecycle(args []string, stdin, stdout, stderr *os.File) int {
	r, err := Parse(args)
	if err == nil && (os.Geteuid() != 0 || r.Action != "start" && r.Action != "restart") {
		err = fmt.Errorf("host lifecycle transport requires root and start or restart")
	}
	if err != nil {
		fmt.Fprintf(stderr, "mdocker: %v\n", err)
		return 125
	}
	return manageOperation(r, stderr, func(ctx context.Context) (int, error) {
		preflight := func(ctx context.Context, ports []config.PortMapping) error {
			return remote.CheckHostPorts(ctx, stdin, stdout, ports)
		}
		var record container.Record
		if r.Action == "start" {
			record, err = container.StartExistingWithPreflight(ctx, r.Reference, preflight)
		} else {
			record, err = container.RestartWithPreflight(ctx, r.Reference, r.StopTimeout, preflight)
		}
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
		return 0, err
	})
}
