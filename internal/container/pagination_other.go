//go:build !linux

package container

import "context"

func (*Store) ListPage(context.Context, RecordPageOptions) (RecordPage, error) {
	return RecordPage{}, errUnsupportedStore
}

func ListPage(context.Context, RecordPageOptions) (RecordPage, error) {
	return RecordPage{}, errUnsupportedStore
}
