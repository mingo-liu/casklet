package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/network"
	"io"
	"text/tabwriter"
)

func executeNetwork(ctx context.Context, r Request, stdout io.Writer) (int, error) {
	store, err := network.OpenStore()
	if err != nil {
		return 125, err
	}
	switch r.Action {
	case "network-create":
		record, err := store.Create(ctx, r.Reference)
		if err != nil {
			return 125, err
		}
		_, err = fmt.Fprintln(stdout, record.Name)
		return 0, err
	case "network-inspect":
		record, err := store.Inspect(ctx, r.Reference)
		if err != nil {
			return 125, err
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return 0, enc.Encode(record)
	case "network-rm":
		if err := store.Remove(ctx, r.Reference, container.NetworkReferenced); err != nil {
			return 125, err
		}
		_, err = fmt.Fprintln(stdout, r.Reference)
		return 0, err
	case "network-ls":
		records, err := store.List(ctx)
		if err != nil {
			return 125, err
		}
		if r.JSON {
			return 0, json.NewEncoder(stdout).Encode(records)
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSUBNET\tGATEWAY\tBRIDGE")
		for _, record := range records {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", record.Name, record.Subnet, record.Gateway, record.Bridge)
		}
		return 0, w.Flush()
	}
	return 125, fmt.Errorf("unknown network action %q", r.Action)
}
