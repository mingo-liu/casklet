//go:build linux || darwin

package remote

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Inherited descriptors may not be registered with Go's poller. Rewrap a
// nonblocking duplicate so Close wakes Read, then restore the caller's shared
// status flags after the handshake reader has finished.
func portCheckFile(original *os.File) (*os.File, func(), error) {
	raw, err := original.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	var (
		duplicate  = -1
		flags      int
		prepareErr error
	)
	if err := raw.Control(func(fd uintptr) {
		flags, prepareErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if prepareErr != nil {
			return
		}
		duplicate, prepareErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
		if prepareErr == nil {
			prepareErr = unix.SetNonblock(duplicate, true)
		}
	}); err != nil {
		prepareErr = errors.Join(prepareErr, err)
	}
	if prepareErr != nil {
		if duplicate >= 0 {
			_ = unix.Close(duplicate)
		}
		return nil, nil, prepareErr
	}
	stream := os.NewFile(uintptr(duplicate), original.Name())
	release := func() {
		_ = stream.Close()
		_ = raw.Control(func(fd uintptr) { _, _ = unix.FcntlInt(fd, unix.F_SETFL, flags) })
	}
	// Regular files and some devices cannot be polled. Preserve their ordinary
	// reader behavior rather than promising that Close will interrupt a syscall.
	if err := stream.SetReadDeadline(time.Time{}); err != nil {
		release()
		if errors.Is(err, os.ErrNoDeadline) {
			return nil, func() {}, nil
		}
		return nil, nil, err
	}
	return stream, release, nil
}
