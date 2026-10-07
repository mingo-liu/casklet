package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
)

type Request struct {
	Action      string
	Config      config.Config
	Exec        config.Exec
	Detach      bool
	Name        string
	All         bool
	JSON        bool
	Reference   string
	Tail        int
	Follow      bool
	StopTimeout *time.Duration
	Interval    time.Duration
}

func Parse(args []string) (request Request, err error) {
	defer func() {
		if err != nil {
			err = argumentError(args, err)
		}
	}()
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return Request{Action: "help"}, nil
	}
	r := Request{Action: args[0]}
	if r.Action == "image" {
		return parseImage(r, args[1:])
	}
	if r.Action == "exec" {
		return parseExec(r, args[1:])
	}
	switch r.Action {
	case "wait", "start", "restart", "ps", "stop", "logs", "rm", "inspect", "stats":
		return parseManagement(r, args[1:])
	}
	if r.Action != "run" && r.Action != "doctor" {
		return r, fmt.Errorf("unknown command %q", r.Action)
	}
	return parseRun(r, args[1:])
}

// expandTerminalFlags accepts the common -it spelling without changing flag
// values or positional arguments. Other short flag combinations stay invalid.
func expandTerminalFlags(fs *flag.FlagSet, options []string) []string {
	expanded := make([]string, 0, len(options)+1)
	for i := 0; i < len(options); i++ {
		arg := options[i]
		if arg == "-it" || arg == "-ti" {
			expanded = append(expanded, "-i", "-t")
			continue
		}
		expanded = append(expanded, arg)
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			expanded = append(expanded, options[i+1:]...)
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(name)
		if f == nil || hasValue {
			continue
		}
		boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
		if ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 < len(options) {
			i++
			expanded = append(expanded, options[i])
		}
	}
	return expanded
}
