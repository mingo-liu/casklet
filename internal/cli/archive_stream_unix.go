//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Inherited standard descriptors are normally blocking and os.NewFile does not
// register them with Go's poller. Closing such a File cannot wake a blocked
// read/write syscall. Rewrap a nonblocking duplicate so cancellation wakes I/O;
// keep the caller's File open and restore its shared status flags on return.
func cancelableArchiveFile(ctx context.Context, original *os.File) (*os.File, func(), error) {
	raw, err := original.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	duplicate, flags := -1, 0
	var prepareErr error
	if err := raw.Control(func(fd uintptr) {
		flags, prepareErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if prepareErr != nil {
			return
		}
		duplicate, prepareErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
		if prepareErr != nil {
			return
		}
		prepareErr = unix.SetNonblock(duplicate, true)
	}); err != nil {
		prepareErr = errors.Join(prepareErr, err)
	}
	if prepareErr != nil {
		if duplicate >= 0 {
			unix.Close(duplicate)
		}
		return nil, nil, prepareErr
	}
	stream := os.NewFile(uintptr(duplicate), original.Name())
	if stream == nil {
		unix.Close(duplicate)
		return nil, nil, errors.New("cannot prepare archive descriptor")
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			stream.Close()
		case <-done:
		}
	}()
	return stream, func() {
		close(done)
		<-finished
		stream.Close()
		_ = raw.Control(func(fd uintptr) { _, _ = unix.FcntlInt(fd, unix.F_SETFL, flags) })
	}, nil
}
