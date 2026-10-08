package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mingo-liu/casklet/internal/storage"
	"io"
	"text/tabwriter"
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
	fmt.Fprintf(w, "VM FILESYSTEM\tBYTES\nTotal\t%d\nFree\t%d\nAvailable\t%d\n\nTYPE\tALLOCATED BYTES\n", fs.TotalBytes, fs.FreeBytes, fs.AvailableBytes)
	for _, category := range report.Categories {
		fmt.Fprintf(w, "%s\t%d\n", category.Kind, category.AllocatedBytes)
	}
	if fs.LowSpace {
		fmt.Fprintln(w, "\nLow disk space: less than 1 GiB or 10% available. Preview unused images with casklet image prune --dry-run.")
	}
	return 0, w.Flush()
}
