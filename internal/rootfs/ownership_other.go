//go:build !linux && !darwin

package rootfs

import "io/fs"

func Ownership(info fs.FileInfo) (uint32, uint32) { return 0, 0 }
