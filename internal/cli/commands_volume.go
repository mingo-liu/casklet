package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/volume"
	"io"
	"text/tabwriter"
)

func executeVolume(ctx context.Context, r Request, stdout io.Writer) (int, error) {
	store, err := volume.OpenStore()
	if err != nil {
		return 125, err
	}
	switch r.Action {
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
