//go:build !linux

package template

import (
	"context"
	"errors"
	"io"
)

func lockBuiltin(context.Context, bool) (io.Closer, error) {
	return nil, errors.New("builtin template leases require Linux")
}
