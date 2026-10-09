package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/mingo-liu/casklet/internal/image"
)

func parseImageCache(r Request, args []string) (Request, error) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return helpRequest([]string{"image", "cache"})
	}
	if len(args) == 0 {
		return r, errors.New("image cache requires ls, prune, or limit")
	}
	topic := "image cache " + args[0]
	r.Action = "image-cache-" + args[0]
	fs := flag.NewFlagSet(topic, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&r.JSON, "json", false, "print JSON cache data")
	switch args[0] {
	case "ls", "limit":
	case "prune":
		fs.BoolVar(&r.DryRun, "dry-run", false, "preview idle compressed cache data")
	default:
		return r, fmt.Errorf("unknown image cache command %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp(topic, fs, args[1:])
		}
		return r, err
	}
	if args[0] != "limit" {
		if fs.NArg() != 0 {
			return r, errors.New("image cache ls and prune take no positional arguments")
		}
		return r, nil
	}
	if fs.NArg() != 1 {
		return r, errors.New("image cache limit requires SIZE: bytes, binary k/m/g, or 0 for unlimited")
	}
	if fs.Arg(0) == "0" {
		r.CacheLimit = 0
		return r, nil
	}
	limit, err := ParseMemory(fs.Arg(0))
	if err != nil {
		return r, errors.New("invalid cache limit: use positive bytes, binary k/m/g, or 0 for unlimited")
	}
	r.CacheLimit = limit
	return r, nil
}

func writeCacheReport(out io.Writer, report image.CacheReport, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(report)
	}
	limit := displayImageSize(report.MaxBytes)
	if report.MaxBytes == 0 {
		limit = "unlimited"
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintf(w, "LIMIT\tCOMPRESSED BYTES\tALLOCATED BYTES\tIDLE ALLOCATED BYTES\n%s\t%d\t%d\t%d\n\nDIGEST\tSIZE\tALLOCATED BYTES\tLAST USED\tIN USE\n", limit, report.SizeBytes, report.AllocatedBytes, report.ReclaimableBytes); err != nil {
		return err
	}
	for _, entry := range report.Entries {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%t\n", displayID(entry.Digest), displayImageSize(entry.SizeBytes), entry.AllocatedBytes, entry.LastUsedAt.Format(time.RFC3339), entry.InUse); err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeCachePrune(out io.Writer, result image.CachePruneResult, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	verb := "Reclaimed"
	if result.DryRun {
		verb = "Would reclaim"
	}
	_, err := fmt.Fprintf(out, "%s %d allocated bytes from %d blobs and %d staging files\n", verb, result.ReclaimedBytes, result.Blobs, result.StagingFiles)
	return err
}
