//go:build linux || darwin

package rootfs

import (
	"errors"
	"io/fs"
	"syscall"
)

// Ownership reads the numeric owner without resolving a symlink.
func Ownership(info fs.FileInfo) (uint32, uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, 0, errors.New("file ownership metadata is unavailable")
	}
	return stat.Uid, stat.Gid, nil
}
