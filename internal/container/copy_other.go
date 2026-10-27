//go:build !linux

package container

import (
	"context"
	"os"
)

func Copy(context.Context, string, string, bool, *os.File, *os.File, *os.File) error {
	return errUnsupportedStore
}
func CopyInit() int { return 125 }
