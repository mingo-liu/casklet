package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
)

func executeImage(ctx context.Context, r Request, stdout io.Writer, input ...io.Reader) (int, error) {
	store, err := image.OpenStore()
	if err != nil {
		return 125, err
	}
	if r.Action == "image-load" || r.Action == "image-save" {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				if r.Action == "image-save" {
					if f, ok := stdout.(*os.File); ok {
						f.Close()
					}
				}
				if r.Action == "image-load" && len(input) > 0 {
					if f, ok := input[0].(*os.File); ok {
						f.Close()
					}
				}
			case <-done:
			}
		}()
	}
	switch r.Action {
	case "image-load":
		if len(input) == 0 {
			return 125, errors.New("image load requires an OCI tar archive on stdin")
		}
		var record image.Record
		record, err = store.Load(ctx, input[0])
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "image-save":
		err = store.Save(ctx, r.Reference, stdout)

	case "image-cache-ls", "image-cache-limit":
		var report image.CacheReport
		if r.Action == "image-cache-limit" {
			report, err = store.SetCacheLimit(ctx, r.CacheLimit)
		} else {
			report, err = store.CacheUsage(ctx)
		}
		if err == nil {
			err = writeCacheReport(stdout, report, r.JSON)
		}
	case "image-cache-prune":
		var result image.CachePruneResult
		result, err = store.PruneCache(ctx, r.DryRun)
		if writeErr := writeCachePrune(stdout, result, r.JSON); writeErr != nil {
			return 125, writeErr
		}
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
	case "image-prune":
		var records []image.Record
		records, err = store.Prune(ctx, r.DryRun, container.ImageReferenced)
		for _, record := range records {
			if _, writeErr := fmt.Fprintln(stdout, record.ID); writeErr != nil {
				return 125, writeErr
			}
		}
	case "image-rm":
		err = store.Remove(ctx, r.Reference, container.ImageReferenced)
		if err == nil {
			_, err = fmt.Fprintln(stdout, r.Reference)
		}
	}
	return 0, err
}
