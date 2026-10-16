//go:build linux

package cli

import (
	"github.com/mingo-liu/casklet/internal/storage"
	"golang.org/x/sys/unix"
)

func doctorFilesystem() (storage.Filesystem, error) {
	var st unix.Statfs_t
	if err := unix.Statfs("/var/lib", &st); err != nil {
		return storage.Filesystem{}, err
	}
	result := storage.Filesystem{TotalBytes: st.Blocks * uint64(st.Bsize), FreeBytes: st.Bfree * uint64(st.Bsize), AvailableBytes: st.Bavail * uint64(st.Bsize)}
	result.LowSpace = result.AvailableBytes < 1<<30 || result.AvailableBytes < result.TotalBytes/10
	return result, nil
}
