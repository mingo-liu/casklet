package cli

import (
	"errors"
	"flag"
	"fmt"
	"github.com/mingo-liu/casklet/internal/config"
	"io"
)

func parseVolume(r Request, args []string) (Request, error) {
	if len(args) == 0 {
		return r, errors.New("volume requires create, ls, inspect, rm, export, or restore")
	}
	if args[0] == "--help" || args[0] == "-h" {
		if len(args) != 1 {
			return r, errors.New("volume help takes no arguments")
		}
		return helpRequest([]string{"volume"})
	}
	r.Action = "volume-" + args[0]
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "ls":
		fs.BoolVar(&r.JSON, "json", false, "print JSON volumes")
	case "create", "inspect", "rm", "export", "restore":
	default:
		return r, fmt.Errorf("unknown volume command %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("volume "+args[0], fs, args[1:])
		}
		return r, err
	}
	if args[0] == "ls" {
		if fs.NArg() != 0 {
			return r, errors.New("volume ls takes no arguments")
		}
		return r, nil
	}
	if fs.NArg() != 1 {
		return r, errors.New("volume command requires exactly one name")
	}
	r.Reference = fs.Arg(0)
	return r, config.ValidateVolumeName(r.Reference)
}
