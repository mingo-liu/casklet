package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/volume"
	"io"
	"os"
	"text/tabwriter"
)

func executeVolume(ctx context.Context, r Request, stdout io.Writer, input ...io.Reader) (int, error) {
	store, err := volume.OpenStore()
	if err != nil {
		return 125, err
	}
	if r.Action == "volume-export" || r.Action == "volume-restore" {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				if r.Action == "volume-export" {
					if f, ok := stdout.(*os.File); ok {
						f.Close()
					}
				}
				if r.Action == "volume-restore" && len(input) > 0 {
					if f, ok := input[0].(*os.File); ok {
						f.Close()
					}
				}
			case <-done:
			}
		}()
	}
	switch r.Action {
	case "volume-export":
		err = store.Export(ctx, r.Reference, stdout)
	case "volume-restore":
		if len(input) == 0 {
			return 125, errors.New("volume restore requires a tar archive on stdin")
		}
		var record volume.Record
		record, err = store.Restore(ctx, r.Reference, input[0])
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.Name)
		}
	case "volume-create":
		var record volume.Record
		record, err = store.Create(ctx, r.Reference)
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.Name)
		}
	case "volume-inspect":
		var record volume.Record
		record, err = store.Inspect(ctx, r.Reference)
		if err == nil {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			err = enc.Encode(record)
		}
	case "volume-rm":
		err = store.Remove(ctx, r.Reference, container.VolumeReferenced)
		if err == nil {
			_, err = fmt.Fprintln(stdout, r.Reference)
		}
	case "volume-ls":
		var records []volume.Record
		records, err = store.List(ctx)
		if err == nil {
			if r.JSON {
				err = json.NewEncoder(stdout).Encode(records)
			} else {
				w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tCREATED")
				for _, record := range records {
					fmt.Fprintf(w, "%s\t%s\n", record.Name, record.CreatedAt.Format("2006-01-02T15:04:05Z"))
				}
				err = w.Flush()
			}
		}
	}
	return 0, err
}
