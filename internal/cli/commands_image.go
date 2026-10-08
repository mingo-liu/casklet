package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
)

func executeImage(ctx context.Context, r Request, stdout io.Writer) (int, error) {
	store, err := image.OpenStore()
	if err != nil {
		return 125, err
	}
	switch r.Action {
	case "image-pull":
		var record image.Record
		record, err = store.Pull(ctx, r.Reference)
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "image-import":
		var record image.Record
		record, err = store.Import(ctx, r.Reference)
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "image-ls":
		var records []image.Record
		records, err = store.List(ctx)
		if err == nil {
			err = writeImages(stdout, records, r.JSON)
		}
	case "image-rm":
		err = store.Remove(ctx, r.Reference, container.ImageReferenced)
		if err == nil {
			_, err = fmt.Fprintln(stdout, r.Reference)
		}
	}
	return 0, err
}
