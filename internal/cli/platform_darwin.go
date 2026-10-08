//go:build darwin

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mingo-liu/casklet/internal/machine"
)

func platformArguments(args []string) []string {
	if len(args) == 0 || (args[0] != "run" && args[0] != "doctor") {
		return args
	}
	booleans := map[string]bool{"d": true, "detach": true, "i": true, "interactive": true, "t": true, "tty": true, "it": true, "ti": true, "read-only": true, "rootless": true, "userns": true}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if len(arg) < 2 || arg[0] != '-' || arg == "--" {
			break
		}
		name := arg[1:]
		if name[0] == '-' {
			name = name[1:]
		}
		if name == "" || name[0] == '-' {
			break
		}
		name, _, inline := strings.Cut(name, "=")
		if name == "rootfs" || name == "image" {
			return args
		}
		if !inline && !booleans[name] {
			i++
		}
	}
	return append([]string{args[0], "--rootfs", machine.BuiltinRootFS}, args[1:]...)
}

func platformUsage(topic string) (string, error) {
	if hostHelpTopic(topic) {
		return machine.Help(topic)
	}
	return scopedUsage(topic, true)
}

func executeHostCommand(args []string, stdin, stdout, stderr *os.File) (bool, int) {
	if len(args) == 0 || (args[0] != "machine" && args[0] != "rootfs") {
		return false, 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := machine.HostCommand(ctx, args, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
		return true, 125
	}
	return true, 0
}

func executePlatform(args []string, request Request, stdin, stdout, stderr *os.File) (bool, int) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	paths, err := hostPathArguments(args, request.Action)
	if err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
		return true, 125
	}
	paths = guestProgressArguments(paths, request, stderr)
	code, err := machine.Execute(ctx, machine.Invocation{Args: paths, TTY: request.Config.TTY || request.Exec.TTY, Interactive: request.Config.Interactive || request.Exec.Interactive, Rootless: request.Config.Rootless}, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
	}
	return true, code
}
