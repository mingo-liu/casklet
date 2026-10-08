package cli

import (
	"errors"
	"flag"
	"io"
)

func parseSystem(r Request, args []string) (Request, error) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return helpRequest([]string{"system"})
	}
	if len(args) == 0 || args[0] != "df" {
		return r, errors.New("system requires df")
	}
	fs := flag.NewFlagSet("system df", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&r.JSON, "json", false, "print JSON disk usage")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp("system df", fs, args[1:])
		}
		return r, err
	}
	if fs.NArg() != 0 {
		return r, errors.New("system df takes no positional arguments")
	}
	r.Action = "system-df"
	return r, nil
}
