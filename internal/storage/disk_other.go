//go:build !linux

package storage

import (
	"context"
	"errors"
)

func AllocatedBytes(context.Context, string) (uint64, error) {
	return 0, errors.New("disk allocation measurement requires Linux")
}
func DiskUsage(context.Context) (Report, error) {
	return Report{}, errors.New("disk management requires Linux")
}
