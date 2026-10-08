package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
)

func writeRecords(out io.Writer, records []container.Record, asJSON bool) error {
	if asJSON {
		if records == nil {
			records = []container.Record{}
		}
		return json.NewEncoder(out).Encode(records)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tSTATUS\tHEALTH\tEXIT\tCREATED\tCOMMAND"); err != nil {
		return err
	}
	for _, record := range records {
		exit := "-"
		if record.ExitCode != nil {
			exit = strconv.Itoa(*record.ExitCode)
		}
		health := "-"
		if record.Health != nil {
			health = record.Health.Status
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", record.ID, record.Name, record.State, health, exit, record.CreatedAt.UTC().Format(time.RFC3339), displayCommand(record.Command)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func displayCommand(command []string) string {
	parts := make([]string, len(command))
	for i, arg := range command {
		if arg == "" || strings.IndexFunc(arg, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			parts[i] = strconv.Quote(arg)
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " ")
}

func writeStats(out io.Writer, stats container.Statistics, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(stats)
	}
	memory, cpu := "N/A", "N/A"
	if stats.MemoryBytes != nil {
		memory = strconv.FormatUint(*stats.MemoryBytes, 10)
	}
	if stats.CPUPercent != nil {
		cpu = fmt.Sprintf("%.2f%%", *stats.CPUPercent)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tSTATUS\tMEMORY (BYTES)\tLIMIT (BYTES)\tCPU"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", stats.ID, stats.Name, stats.State, memory, stats.MemoryLimitBytes, cpu); err != nil {
		return err
	}
	return w.Flush()
}

func writeImages(out io.Writer, records []image.Record, asJSON bool) error {
	if asJSON {
		if records == nil {
			records = []image.Record{}
		}
		return json.NewEncoder(out).Encode(records)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tARCHITECTURE\tSIZE (BYTES)\tCREATED\tREFERENCES"); err != nil {
		return err
	}
	for _, record := range records {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", record.ID, record.Architecture, record.SizeBytes, record.CreatedAt.UTC().Format(time.RFC3339), strings.Join(record.References, ", ")); err != nil {
			return err
		}
	}
	return w.Flush()
}
