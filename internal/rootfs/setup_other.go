//go:build !linux

package rootfs

import "errors"

func Setup(path string, readOnly bool) error {
	return errors.New("rootfs setup requires Linux")
}
