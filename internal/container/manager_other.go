//go:build !linux

package container

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/mingo-liu/mini-docker/internal/config"
)

func Start(context.Context, config.Config, string) (Record, error) {
	return Record{}, errUnsupportedStore
}
func List(context.Context, bool) ([]Record, error)             { return nil, errUnsupportedStore }
func Stop(context.Context, string) (Record, error)             { return Record{}, errUnsupportedStore }
func Logs(context.Context, string, int, bool, io.Writer) error { return errUnsupportedStore }
func Remove(context.Context, string) error                     { return errUnsupportedStore }
func Supervisor(string) int {
	fmt.Fprintln(os.Stderr, "mini-docker:", errUnsupportedStore)
	return 125
}
