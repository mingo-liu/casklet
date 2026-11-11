package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/mingo-liu/casklet/internal/storage"
)

func executeSystem(ctx context.Context, r Request, out io.Writer) (int, error) {
	report, err := storage.DiskUsage(ctx)
	if err != nil {
		return 125, err
	}
	if r.JSON {
		return 0, json.NewEncoder(out).Encode(report)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fs := report.Filesystem
	if _, err := fmt.Fprintf(w, "VM FILESYSTEM\tBYTES\nTotal\t%d\nFree\t%d\nAvailable\t%d\n\nTYPE\tALLOCATED BYTES\n", fs.TotalBytes, fs.FreeBytes, fs.AvailableBytes); err != nil {
		return 125, err
	}
	for _, category := range report.Categories {
		if _, err := fmt.Fprintf(w, "%s\t%d\n", category.Kind, category.AllocatedBytes); err != nil {
			return 125, err
		}
	}
	if fs.LowSpace {
		if _, err := fmt.Fprintln(w, "\nLow disk space: less than 1 GiB or 10% available. Preview unused images with casklet image prune --dry-run."); err != nil {
			return 125, err
		}
	}
	return 0, w.Flush()
}
