//go:build !linux

package rootfs

import "errors"

func Setup(path string) error {
	return errors.New("rootfs setup requires Linux")
}
