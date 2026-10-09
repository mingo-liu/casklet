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
	if _, err := fmt.Fprintln(w, "IMAGE\tID\tARCHITECTURE\tSIZE\tCREATED"); err != nil {
		return err
	}
	for _, record := range records {
		references := record.References
		if len(references) == 0 {
			references = []string{"<none>"}
		}
		id := strings.TrimPrefix(record.ID, "sha256:")
		if len(id) > 12 {
			id = id[:12]
		}
		for _, reference := range references {
			name := strings.TrimPrefix(reference, "index.docker.io/")
			if name != reference {
				name = strings.TrimPrefix(name, "library/")
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, id, record.Architecture, displayImageSize(record.SizeBytes), record.CreatedAt.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
	}
	return w.Flush()
}

func displayImageSize(bytes int64) string {
	if bytes < 1000 {
		return strconv.FormatInt(bytes, 10) + "B"
	}
	units := [...]string{"B", "kB", "MB", "GB", "TB", "PB", "EB"}
	value, unit := float64(bytes), 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	// Promote values that would otherwise round to 1000 of the smaller unit.
	if value >= 999.5 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	precision := 2
	if value >= 100 {
		precision = 0
	} else if value >= 10 {
		precision = 1
	}
	size := strconv.FormatFloat(value, 'f', precision, 64)
	if precision > 0 {
		size = strings.TrimRight(strings.TrimRight(size, "0"), ".")
	}
	return size + units[unit]
}
