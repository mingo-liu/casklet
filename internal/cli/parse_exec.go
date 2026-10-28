package cli

import (
	"errors"
	"flag"
	"io"
	"path"
	"strings"

	"github.com/mingo-liu/casklet/internal/project"
)

func parseExec(r Request, args []string) (Request, error) {
	return parseExecOperands(r, args, 1)
}

func parseExecOperands(r Request, args []string, operands int) (Request, error) {
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.String("env-file", "", "environment assignments file")
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
			return parseFlagHelp(strings.Replace(r.Action, "project-", "project ", 1), fs, args)
		}
		return r, err
	}
	var workdirSpecified bool
	fs.Visit(func(f *flag.Flag) {
		workdirSpecified = workdirSpecified || f.Name == "workdir"
	})
	if fs.NArg() != operands || fs.Arg(0) == "" {
		return r, errors.New("exec requires its container, or project and service, before --; flags must precede operands")
	}
	r.Reference = fs.Arg(0)
	if operands == 2 {
		if project.ValidateName(r.Reference) != nil || project.ValidateName(fs.Arg(1)) != nil {
			return r, errors.New("project exec requires a valid project and logical service name")
		}
		r.Services = []string{fs.Arg(1)}
	}
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
