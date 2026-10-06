package runtime

import (
	"os"

	"github.com/mingo-liu/mini-docker/internal/cgroup"
	"github.com/mingo-liu/mini-docker/internal/config"
)

// ExecResources pins a live container's namespaces, root, and executable.
// Namespace descriptors are ordered mount, UTS, IPC, network, and PID.
type ExecResources struct {
	Namespaces []*os.File
	Root       *os.File
	Executable *os.File
	Group      *cgroup.Group
	Config     config.Config
}

// Executor owns the execution endpoint throughout the container lifetime.
type Executor interface {
	Start(ExecResources) error
	Close() error
}
