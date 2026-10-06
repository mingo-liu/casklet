//go:build !linux

package container

import (
	"context"
	"os"
)

func (*Store) AcquireOperation(context.Context, string) (*os.File, error) {
	return nil, errUnsupportedStore
}
func (*Store) Completion(context.Context, string, uint64) (ExecutionResult, bool, error) {
	return ExecutionResult{}, false, errUnsupportedStore
}
