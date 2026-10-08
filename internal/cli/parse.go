package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

type Request struct {
	Action      string
	HelpTopic   string
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
	Entrypoint  *string
	Progress    string
}

func Parse(args []string) (request Request, err error) {
	defer func() {
		if err != nil {
			err = argumentError(args, err)
		}
	}()
	if len(args) == 0 {
		return Request{Action: "help"}, nil
	}
	if args[0] == "help" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			return Request{Action: "help", HelpTopic: "help"}, nil
		}
		return helpRequest(args[1:])
	}
	if args[0] == "--help" || args[0] == "-h" {
		if len(args) != 1 {
			return Request{}, fmt.Errorf("%s takes no arguments; use casklet help COMMAND for a topic", args[0])
		}
		return Request{Action: "help"}, nil
	}
	if (args[0] == "machine" || args[0] == "rootfs") && len(args) > 1 && (args[len(args)-1] == "--help" || args[len(args)-1] == "-h") {
		return helpRequest(args[:len(args)-1])
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

// parseFlagHelp is called only after flag.Parse returns ErrHelp. Walk option
// values so a literal -h or --help cannot be mistaken for the help switch.
func parseFlagHelp(topic string, fs *flag.FlagSet, args []string) (Request, error) {
	for i := 0; i < len(args); i++ {
		name, _, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if !inline && (name == "h" || name == "help") {
			if i != len(args)-1 {
				return Request{}, fmt.Errorf("%s help accepts no additional arguments; use casklet help %s", topic, topic)
			}
			return Request{Action: "help", HelpTopic: topic}, nil
		}
		f := fs.Lookup(name)
		if f == nil || inline {
			continue
		}
		boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
		if !ok || !boolean.IsBoolFlag() {
			i++
		}
	}
	return Request{Action: "help", HelpTopic: topic}, nil
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
