//go:build !linux

package runtime

import (
	"errors"
	"fmt"
	"os"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func Check(_ string) error {
	return errors.New("container execution requires Linux; run mini-docker inside the development VM")
}

func Run(_ config.Config, _, _, _ *os.File) (int, error) {
	return 125, Check("")
}

func Init() int {
	fmt.Fprintln(os.Stderr, "mini-docker: container init requires Linux")
	return 125
}

func Probe() int { return Init() }
