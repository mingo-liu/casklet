//go:build !linux && !darwin

package copyfs

import (
	"errors"
	"os"
)

func openRegular(*os.Root, string) (*os.File, error) {
	return nil, errors.New("file copying requires macOS or Linux")
}
func createTemporary(*os.Root) (*os.File, error) {
	return nil, errors.New("file copying requires macOS or Linux")
}
