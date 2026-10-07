//go:build darwin

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mingo-liu/mini-docker/internal/machine"
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

func platformUsage() string {
	text, _, _ := strings.Cut(usage, "Containers require Linux")
	_, management, _ := strings.Cut(usage, "Management flags must precede")
	return strings.Replace(text, "mdocker run (--rootfs DIRECTORY | --image ID)", "mdocker run [--rootfs DIRECTORY | --image ID]", 1) + `macOS host commands:
  mdocker machine init [--cpus N] [--memory GiB] [--disk GiB] [--mount DIRECTORY ...]
  mdocker machine start
  mdocker machine stop
  mdocker machine status
  mdocker rootfs DIRECTORY

Requires macOS 13.5+ and Lima 2.0+. Install Lima with: brew install lima
The first container command creates a dedicated Linux VM automatically.
The default filesystem is the VM's built-in BusyBox template.
Your home directory is shared with the VM; use machine init --mount for other directories.
Container state and images live in the VM. Rootless identities refer to the VM user.
Published ports support 0.0.0.0 and 127.0.0.1 on the Mac, for TCP and UDP.
Stopping the machine terminates its containers and preserves their files.
` + "\nManagement flags must precede" + management
}

func executeHostCommand(args []string, stdin, stdout, stderr *os.File) (bool, int) {
	if len(args) == 0 || (args[0] != "machine" && args[0] != "rootfs") {
		return false, 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := machine.HostCommand(ctx, args, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "mdocker: %v\n", err)
		return true, 125
	}
	return true, 0
}

func executePlatform(args []string, request Request, stdin, stdout, stderr *os.File) (bool, int) {
	if request.Action == "help" {
		fmt.Fprint(stdout, platformUsage())
		return true, 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	paths, err := hostPathArguments(args, request.Action)
	if err != nil {
		fmt.Fprintf(stderr, "mdocker: %v\n", err)
		return true, 125
	}
	code, err := machine.Execute(ctx, machine.Invocation{Args: paths, TTY: request.Config.TTY || request.Exec.TTY, Interactive: request.Config.Interactive || request.Exec.Interactive, Rootless: request.Config.Rootless}, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "mdocker: %v\n", err)
	}
	return true, code
}
