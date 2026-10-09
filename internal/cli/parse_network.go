package cli

import (
	"errors"
	"flag"
	"fmt"
	"github.com/mingo-liu/casklet/internal/config"
	"io"
)

func parseNetwork(r Request, args []string) (Request, error) {
	if len(args) == 0 {
		return r, errors.New("network requires create, ls, inspect, or rm")
	}
	if args[0] == "--help" || args[0] == "-h" {
		if len(args) != 1 {
			return r, errors.New("network help takes no arguments")
		}
		return helpRequest([]string{"network"})
	}
	r.Action = "network-" + args[0]
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "ls":
		fs.BoolVar(&r.JSON, "json", false, "print JSON networks")
	case "create", "inspect", "rm":
	default:
		return r, fmt.Errorf("unknown network command %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("network "+args[0], fs, args[1:])
		}
		return r, err
	}
	if args[0] == "ls" {
		if fs.NArg() != 0 {
			return r, errors.New("network ls takes no arguments")
		}
		return r, nil
	}
	if fs.NArg() != 1 {
		return r, errors.New("network command requires exactly one name")
	}
	r.Reference = fs.Arg(0)
	return r, config.ValidateNetworkName(r.Reference)
}
