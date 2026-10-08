//go:build !linux

package rootfs

import (
	"errors"

	"github.com/mingo-liu/casklet/internal/config"
)

func Setup(path string, readOnly bool, mounts ...config.BindMount) error {
	return errors.New("rootfs setup requires Linux")
}
