package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/mingo-liu/casklet/internal/container"
)

func executeOperation(ctx context.Context, r Request, stdout io.Writer) (int, error) {
	var err error
	returnCode := 0
	switch r.Action {
	case "system-df":
		return executeSystem(ctx, r, stdout)
	case "volume-create", "volume-ls", "volume-inspect", "volume-rm":
		return executeVolume(ctx, r, stdout)
	case "image-import", "image-pull", "image-ls", "image-rm", "image-prune":
		return executeImage(ctx, r, stdout)
	case "run":
		var record container.Record
		record, err = container.Start(ctx, r.Config, r.Name)
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "ps":
		var records []container.Record
		records, err = container.List(ctx, r.All)
		if err == nil {
			err = writeRecords(stdout, records, r.JSON)
		}
	case "inspect":
		var inspection container.Inspection
		inspection, err = container.Inspect(ctx, r.Reference)
		if err == nil {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			err = encoder.Encode(inspection)
		}
	case "stats":
		var stats container.Statistics
		stats, err = container.Stats(ctx, r.Reference, r.Interval)
		if err == nil {
			err = writeStats(stdout, stats, r.JSON)
		}
	case "wait":
		returnCode, err = container.Wait(ctx, r.Reference)
		if err == nil {
			_, err = fmt.Fprintln(stdout, returnCode)
		}
	case "start", "restart":
		var record container.Record
		if r.Action == "start" {
			record, err = container.StartExisting(ctx, r.Reference)
		} else {
			record, err = container.Restart(ctx, r.Reference, r.StopTimeout)
		}
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "stop":
		var record container.Record
		record, err = container.StopWithTimeout(ctx, r.Reference, r.StopTimeout)
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "logs":
		err = container.Logs(ctx, r.Reference, r.Tail, r.Follow, stdout)
	case "rm":
		err = container.Remove(ctx, r.Reference)
		if err == nil {
			_, err = fmt.Fprintln(stdout, r.Reference)
		}
	default:
		return 125, fmt.Errorf("unknown management command %q", r.Action)
	}
	return returnCode, err
}
