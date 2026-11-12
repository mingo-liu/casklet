//go:build !linux && !darwin

package ipc

import "os"

// InterruptibleFile supports standard-descriptor transport on Darwin and Linux.
func InterruptibleFile(*os.File) (*os.File, func(), error) {
	return nil, func() {}, nil
}
