//go:build !linux

package network

import (
	"context"
	"errors"
	"os"
)

var errNamedUnsupported = errors.New("named networks require the internal Linux engine")

type Store struct{}

func OpenStore() (*Store, error)                                    { return nil, errNamedUnsupported }
func (*Store) Create(context.Context, string) (Record, error)       { return Record{}, errNamedUnsupported }
func (*Store) List(context.Context) ([]Record, error)               { return nil, errNamedUnsupported }
func (*Store) Inspect(context.Context, string) (Record, error)      { return Record{}, errNamedUnsupported }
func (*Store) Acquire(context.Context, string) (*os.File, error)    { return nil, errNamedUnsupported }
func (*Store) Remove(context.Context, string, ReferenceCheck) error { return errNamedUnsupported }
