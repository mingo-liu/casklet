package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func parseEvents(r Request, args []string) (Request, error) {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&r.JSON, "json", false, "print one JSON object per event")
	fs.BoolVar(&r.Events.Follow, "follow", false, "follow future changes")
	fs.BoolVar(&r.Events.Follow, "f", false, "follow future changes")
	fs.Func("after", "resume after a sequence", func(value string) error {
		number, err := strconv.ParseUint(value, 10, 64)
		if err == nil {
			r.Events.After = &number
		}
		return err
	})
	fs.Func("since", "earliest RFC3339 timestamp", func(value string) error {
		valueTime, err := time.Parse(time.RFC3339Nano, value)
		r.Events.Since = valueTime
		return err
	})
	fs.Func("filter", "event/container/project/service selection (repeatable AND)", func(value string) error {
		key, value, ok := strings.Cut(value, "=")
		if !ok {
			return errors.New("events filters require KEY=VALUE")
		}
		r.Events.Filters = append(r.Events.Filters, container.EventFilter{Key: key, Value: value})
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("events", fs, args)
		}
		return r, err
	}
	if fs.NArg() != 0 {
		return r, errors.New("events accepts flags only")
	}
	return r, r.Events.Validate()
}

func executeEvents(ctx context.Context, r Request, out io.Writer) (int, error) {
	if file, ok := out.(*os.File); ok {
		stream, finish, err := cancelableStreamFile(ctx, file)
		if err != nil {
			return 125, err
		}
		defer finish()
		out = stream
	}
	header := false
	encoder := json.NewEncoder(out)
	return 0, container.Events(ctx, r.Events, func(event container.Event) error {
		if r.JSON {
			return encoder.Encode(event)
		}
		if !header {
			if _, err := fmt.Fprintln(out, "SEQUENCE\tTIME\tID\tNAME\tEVENT\tGENERATION\tSTATE\tHEALTH\tEXIT\tREASON"); err != nil {
				return err
			}
			header = true
		}
		health, exit, reason := event.Health, "-", event.ExitReason
		if health == "" {
			health = "-"
		}
		if reason == "" {
			reason = "-"
		}
		if event.ExitCode != nil {
			exit = strconv.Itoa(*event.ExitCode)
		}
		_, err := fmt.Fprintf(out, "%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", event.Sequence, event.Time.Format(time.RFC3339Nano), displayID(event.ID), event.Name, event.Type, event.Generation, event.State, health, exit, reason)
		return err
	})
}
