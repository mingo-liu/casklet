//go:build !linux

package rootfs

import (
	"errors"

	"github.com/mingo-liu/casklet/internal/config"
)

func Setup(cfg config.Config) error {
	return errors.New("rootfs setup requires Linux")
}
