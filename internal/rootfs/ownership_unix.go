//go:build linux || darwin

package rootfs

import (
	"io/fs"
	"syscall"
)

// Ownership reads the numeric owner without resolving a symlink.
func Ownership(info fs.FileInfo) (uint32, uint32) {
	stat := info.Sys().(*syscall.Stat_t)
	return stat.Uid, stat.Gid
}
