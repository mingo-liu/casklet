//go:build !linux && !darwin

package rootfs

import (
	"errors"
	"io/fs"
)

func Ownership(info fs.FileInfo) (uint32, uint32, error) {
	return 0, 0, errors.New("file ownership requires macOS or Linux")
}
