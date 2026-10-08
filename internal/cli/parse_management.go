package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
)

func parseManagement(r Request, args []string) (Request, error) {
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch r.Action {
	case "stop", "restart":
		fs.Func("timeout", "graceful shutdown override", func(value string) error {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return err
			}
			if err := config.ValidateStopTimeout(duration); err != nil {
				return err
			}
			r.StopTimeout = &duration
			return nil
		})
	case "ps":
		fs.BoolVar(&r.All, "all", false, "include completed containers")
		fs.BoolVar(&r.All, "a", false, "include completed containers")
		fs.BoolVar(&r.JSON, "json", false, "print JSON records")
	case "stats":
		fs.BoolVar(&r.JSON, "json", false, "print JSON statistics")
		fs.DurationVar(&r.Interval, "interval", time.Second, "CPU sampling interval")
	case "logs":
		r.Tail = -1
		fs.IntVar(&r.Tail, "tail", -1, "maximum number of trailing lines")
		fs.BoolVar(&r.Follow, "follow", false, "follow log output")
		fs.BoolVar(&r.Follow, "f", false, "follow log output")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp(r.Action, fs, args)
		}
		return r, err
	}
	if r.Action == "ps" {
		if fs.NArg() != 0 {
			return r, errors.New("ps does not accept positional arguments")
		}
		return r, nil
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return r, fmt.Errorf("%s requires exactly one container ID or name; flags must precede it", r.Action)
	}
	if r.Action == "stats" {
		if err := container.ValidateStatsInterval(r.Interval); err != nil {
			return r, err
		}
	}
	r.Reference = fs.Arg(0)
	if strings.ContainsAny(r.Reference, "/\\\x00") || r.Reference == "." || r.Reference == ".." {
		return r, errors.New("invalid container ID or name")
	}
	if r.Action == "logs" {
		explicitTail := false
		fs.Visit(func(f *flag.Flag) { explicitTail = explicitTail || f.Name == "tail" })
		if (explicitTail && r.Tail < 0) || r.Tail > 1000000 {
			return r, errors.New("--tail must be between 0 and 1000000")
		}
	}
	return r, nil
}
