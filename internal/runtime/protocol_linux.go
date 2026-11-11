//go:build linux

package runtime

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/rootfs"
	"golang.org/x/sys/unix"
)

const (
	namespaceFlags = unix.CLONE_NEWPID | unix.CLONE_NEWNS | unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWNET
	startupLimit   = 30 * time.Second
	stopGrace      = 5 * time.Second
)

var baseEnvironment = []string{"PATH=/bin:/usr/bin", "HOME=/", "LANG=C"}

type message struct {
	Kind        string          `json:"kind"`
	Config      *config.Config  `json:"config,omitempty"`
	Error       string          `json:"error,omitempty"`
	ExitCode    int             `json:"exit_code,omitempty"`
	StopTimeout time.Duration   `json:"stop_timeout,omitempty"`
	ExecEnabled bool            `json:"exec_enabled,omitempty"`
	Overlay     *rootfs.Overlay `json:"overlay,omitempty"`
	DNS         []string        `json:"dns,omitempty"`
	ImageLease  bool            `json:"image_lease,omitempty"`
}

// startControlEvents owns the decoder goroutine for a nonblocking control socket.
// The caller retains the socket; stopping interrupts only its read side.
func startControlEvents(control *os.File) (<-chan message, func()) {
	// Init emits at most ready, started, exited and an error before shutdown.
	events := make(chan message, 4)
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(events)
		decoder := json.NewDecoder(control)
		for {
			var event message
			if err := decoder.Decode(&event); err != nil {
				return
			}
			select {
			case events <- event:
			case <-stopped:
				return
			}
		}
	}()
	var once sync.Once
	return events, func() {
		once.Do(func() {
			close(stopped)
			// Socket read deadlines wake the runtime poller, including a blocked Decode.
			_ = control.SetReadDeadline(time.Now())
		})
		<-done
	}
}
