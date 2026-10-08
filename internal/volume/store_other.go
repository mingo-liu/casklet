//go:build !linux

package volume

import (
	"context"
	"errors"
	"io"
	"os"
)

type Store struct{}

var errUnsupported = errors.New("named volume management requires Linux")

func OpenStore() (*Store, error)                                    { return nil, errUnsupported }
func (*Store) Create(context.Context, string) (Record, error)       { return Record{}, errUnsupported }
func (*Store) List(context.Context) ([]Record, error)               { return nil, errUnsupported }
func (*Store) Inspect(context.Context, string) (Record, error)      { return Record{}, errUnsupported }
func (*Store) Acquire(context.Context, string) (*os.File, error)    { return nil, errUnsupported }
func (*Store) Remove(context.Context, string, ReferenceCheck) error { return errUnsupported }

func (*Store) Export(context.Context, string, io.Writer) error { return errUnsupported }
func (*Store) Restore(context.Context, string, io.Reader) (Record, error) {
	return Record{}, errUnsupported
}
