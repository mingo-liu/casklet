//go:build !linux

package image

import (
	"context"
	"errors"
	"os"
)

type Store struct{}

var errUnsupported = errors.New("local image management requires Linux")

func OpenStore() (*Store, error)                               { return nil, errUnsupported }
func (*Store) Import(context.Context, string) (Record, error)  { return Record{}, errUnsupported }
func (*Store) List(context.Context) ([]Record, error)          { return nil, errUnsupported }
func (*Store) Resolve(context.Context, string) (Record, error) { return Record{}, errUnsupported }
func (*Store) Pull(context.Context, string) (Record, error)    { return Record{}, errUnsupported }
func (*Store) Acquire(context.Context, string) (Record, string, *os.File, error) {
	return Record{}, "", nil, errUnsupported
}
func (*Store) Remove(context.Context, string, ReferenceCheck) error { return errUnsupported }

func (*Store) remove(context.Context, string, ReferenceCheck, bool) error { return errUnsupported }
