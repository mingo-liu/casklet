//go:build linux || darwin

package rootfs

import (
	"os"

	"golang.org/x/sys/unix"
)

func openSource(path string) (*os.File, error) {
	// Nonblocking open prevents a replaced FIFO from hanging preparation.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
