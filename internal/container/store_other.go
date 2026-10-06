//go:build !linux

package container

import (
	"context"
	"errors"
	"os"

	"github.com/mingo-liu/mini-docker/internal/config"
)

type Store struct{}

var errUnsupportedStore = errors.New("container management requires Linux")

func OpenStore() (*Store, error) { return nil, errUnsupportedStore }
func (*Store) Create(context.Context, config.Config, string) (Record, error) {
	return Record{}, errUnsupportedStore
}
func (*Store) Get(context.Context, string) (Record, error)               { return Record{}, errUnsupportedStore }
func (*Store) List(context.Context) ([]Record, error)                    { return nil, errUnsupportedStore }
func (*Store) Update(context.Context, string, func(*Record) error) error { return errUnsupportedStore }
func (*Store) Config(context.Context, string) (config.Config, error) {
	return config.Config{}, errUnsupportedStore
}
func (*Store) AcquireLease(context.Context, string) (*os.File, error) {
	return nil, errUnsupportedStore
}
func (*Store) OpenLog(context.Context, string, bool) (*os.File, error) {
	return nil, errUnsupportedStore
}
func (*Store) Remove(context.Context, string) error { return errUnsupportedStore }
