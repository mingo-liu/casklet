package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/mingo-liu/mini-docker/internal/image"
)

func parseImage(r Request, args []string) (Request, error) {
	if len(args) == 0 {
		return r, errors.New("image requires import, ls, or rm")
	}
	if args[0] == "--help" || args[0] == "-h" {
		if len(args) != 1 {
			return r, errors.New("image help takes no arguments; use mdocker help image COMMAND for a subcommand")
		}
		return Request{Action: "help", HelpTopic: "image"}, nil
	}
	r.Action = "image-" + args[0]
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "ls":
		fs.BoolVar(&r.JSON, "json", false, "print JSON images")
	case "import", "rm":
	default:
		return r, fmt.Errorf("unknown image command %q", args[0])
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("image "+args[0], fs, args[1:])
		}
		return r, err
	}
	if args[0] == "ls" {
		if fs.NArg() != 0 {
			return r, errors.New("image ls does not accept positional arguments")
		}
		return r, nil
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return r, errors.New("image import and rm require exactly one argument")
	}
	r.Reference = fs.Arg(0)
	if args[0] == "rm" {
		if err := image.ValidateID(r.Reference); err != nil {
			return r, err
		}
	}
	return r, nil
}
