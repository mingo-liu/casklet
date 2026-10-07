//go:build linux

package runtime

import (
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"golang.org/x/sys/unix"
)

const (
	namespaceFlags = unix.CLONE_NEWPID | unix.CLONE_NEWNS | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWNET
	startupLimit   = 30 * time.Second
	stopGrace      = 5 * time.Second
)

var baseEnvironment = []string{"PATH=/bin:/usr/bin", "HOME=/", "LANG=C"}

type message struct {
	Kind        string         `json:"kind"`
	Config      *config.Config `json:"config,omitempty"`
	Error       string         `json:"error,omitempty"`
	ExitCode    int            `json:"exit_code,omitempty"`
	StopTimeout time.Duration  `json:"stop_timeout,omitempty"`
	ExecEnabled bool           `json:"exec_enabled,omitempty"`
}
