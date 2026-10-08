package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
	"github.com/mingo-liu/casklet/internal/template"
)

func Execute(args []string, stdin, stdout, stderr *os.File) int {
	if handled, code := executeHostCommand(args, stdin, stdout, stderr); handled {
		return code
	}
	args = platformArguments(args)
	r, err := Parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
		return 125
	}
	if r.Action == "help" {
		text, err := platformUsage(r.HelpTopic)
		if err == nil {
			_, err = fmt.Fprint(stdout, text)
		}
		if err != nil {
			fmt.Fprintf(stderr, "casklet: %v\n", err)
			return 125
		}
		return 0
	}
	if handled, code := executePlatform(args, r, stdin, stdout, stderr); handled {
		return code
	}
	if err := prepareLaunch(args, r); err != nil {
		fmt.Fprintf(stderr, "casklet: %v\n", err)
		return 125
	}
	switch r.Action {
	case "doctor":
		if err := containerruntime.Check(r.Config.RootFS); err != nil {
			fmt.Fprintf(stderr, "casklet: %v\n", err)
			return 125
		}
		fmt.Fprintln(stdout, "All required runtime capabilities are available.")
		return 0
	case "system-df":
		return executeManagement(r, stdout, stderr)
	case "volume-create", "volume-ls", "volume-inspect", "volume-rm":
		return executeManagement(r, stdout, stderr)
	case "image-import", "image-pull", "image-ls", "image-rm", "image-prune", "ps", "stop", "wait", "start", "restart", "logs", "rm", "inspect", "stats":
		return executeManagement(r, stdout, stderr)
	case "exec":
		signals := make(chan os.Signal, 16)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
		defer signal.Stop(signals)
		code, err := container.Exec(context.Background(), r.Reference, r.Exec, stdin, stdout, stderr, signals)
		if err != nil {
			fmt.Fprintf(stderr, "casklet: %v\n", operationError(r, err))
			if code == 0 {
				return 125
			}
		}
		return code
	case "run":
		if r.Config.Image != "" {
			var lease interface{ Close() error }
			// Pulls have their own bounded, cancelable preparation period, before
			// the shorter detached workload startup deadline begins.
			code := manageOperation(Request{Action: "image-pull", Reference: r.Config.Image}, stderr, func(ctx context.Context) (int, error) {
				var err error
				progress := newPullProgress(stderr, r.Progress)
				ctx = image.WithProgress(ctx, progress.observe)
				r.Config, lease, err = template.ResolveExecution(ctx, r.Config, r.Entrypoint)
				return 0, err
			})
			if lease != nil {
				defer lease.Close()
			}
			if code != 0 {
				return code
			}
		}
		if r.Detach {
			return executeManagement(r, stdout, stderr)
		}
		code, err := containerruntime.Run(r.Config, stdin, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "casklet: %v\n", err)
			if code == 0 {
				return 125
			}
		}
		return code
	default:
		fmt.Fprintf(stderr, "casklet: unknown command %q\n", r.Action)
		return 125
	}
}
