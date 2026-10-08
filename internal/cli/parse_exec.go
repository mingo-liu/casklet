package cli

import (
	"errors"
	"flag"
	"io"
	"path"
	"strings"
)

func parseExec(r Request, args []string) (Request, error) {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&r.Exec.Interactive, "interactive", false, "forward stdin")
	fs.BoolVar(&r.Exec.Interactive, "i", false, "forward stdin")
	fs.BoolVar(&r.Exec.TTY, "tty", false, "allocate a terminal")
	fs.BoolVar(&r.Exec.TTY, "t", false, "allocate a terminal")
	fs.Func("env", "environment assignment", func(value string) error {
		r.Exec.Env = append(r.Exec.Env, value)
		return nil
	})
	fs.StringVar(&r.Exec.Workdir, "workdir", "", "working directory")
	fs.DurationVar(&r.Exec.Timeout, "timeout", 0, "command timeout")
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	options := args
	if separator >= 0 {
		options = args[:separator]
		r.Exec.Command = append([]string(nil), args[separator+1:]...)
	}
	if err := fs.Parse(expandTerminalFlags(fs, options)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp(r.Action, fs, args)
		}
		return r, err
	}
	var workdirSpecified bool
	fs.Visit(func(f *flag.Flag) {
		workdirSpecified = workdirSpecified || f.Name == "workdir"
	})
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return r, errors.New("exec requires exactly one container ID or name before --; flags must precede it")
	}
	r.Reference = fs.Arg(0)
	if strings.ContainsAny(r.Reference, "/\\\x00") || r.Reference == "." || r.Reference == ".." {
		return r, errors.New("invalid container ID or name")
	}
	if separator < 0 {
		return r, errors.New("a command is required after --; for example: casklet exec worker -- /bin/echo hello")
	}
	if workdirSpecified && r.Exec.Workdir == "" {
		return r, errors.New("--workdir must be an absolute path")
	}
	if err := r.Exec.Validate(); err != nil {
		return r, err
	}
	if r.Exec.Workdir != "" {
		r.Exec.Workdir = path.Clean(r.Exec.Workdir)
	}
	return r, nil
}
