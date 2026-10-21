//go:build !linux

package container

import "context"

func ListIDs(context.Context, []string) ([]Record, error) {
	return nil, errUnsupportedStore
}
