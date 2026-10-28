package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func selectProjectServices(records []container.Record, name string, services []string) ([]container.Record, error) {
	selected, err := selectProjectLogs(records, name, services)
	if err != nil {
		return nil, err
	}
	for _, record := range selected {
		expected, err := project.ScopedName(name, record.Labels[project.LabelService])
		if err != nil || record.Name != expected {
			return nil, errors.New("project has renamed or unverified service records")
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("project has no matching services")
	}
	return selected, nil
}

func executeProjectLifecycle(ctx context.Context, r Request, records []container.Record, out io.Writer, call project.Call) error {
	selected, err := selectProjectServices(records, r.Reference, r.Services)
	if err != nil {
		return err
	}
	action := strings.TrimPrefix(r.Action, "project-")
	// Config-free commands use creation order; project up owns dependency checks.
	sort.Slice(selected, func(i, j int) bool {
		a, b := selected[i], selected[j]
		less := a.CreatedAt.Before(b.CreatedAt) || a.CreatedAt.Equal(b.CreatedAt) && a.ID < b.ID
		if action == "stop" {
			return b.CreatedAt.Before(a.CreatedAt) || a.CreatedAt.Equal(b.CreatedAt) && b.ID < a.ID
		}
		return less
	})
	var failures []error
	for _, record := range selected {
		if action != "stop" && len(r.Services) == 0 && record.Labels[project.LabelTask] == "true" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		args := []string{action}
		if r.StopTimeout != nil {
			args = append(args, "--timeout", r.StopTimeout.String())
		}
		args = append(args, record.ID)
		if _, err := call(ctx, args...); err != nil {
			failures = append(failures, fmt.Errorf("service %q: %w", record.Labels[project.LabelService], err))
			continue
		}
		if _, err := fmt.Fprintln(out, record.ID); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

func projectExecArguments(ctx context.Context, r Request, backend project.Backend) ([]string, error) {
	records, err := backend.Records(ctx, r.Reference)
	if err != nil {
		return nil, err
	}
	selected, err := selectProjectServices(records, r.Reference, r.Services)
	if err != nil {
		return nil, err
	}
	if len(selected) != 1 {
		return nil, errors.New("project exec requires exactly one service")
	}
	args := []string{"exec"}
	if r.Exec.Interactive {
		args = append(args, "--interactive")
	}
	if r.Exec.TTY {
		args = append(args, "--tty")
	}
	for _, assignment := range r.Exec.Env {
		args = append(args, "--env", assignment)
	}
	if r.Exec.Workdir != "" {
		args = append(args, "--workdir", r.Exec.Workdir)
	}
	if r.Exec.Timeout != 0 {
		args = append(args, "--timeout", r.Exec.Timeout.String())
	}
	args = append(args, selected[0].ID, "--")
	args = append(args, r.Exec.Command...)
	return args, nil
}

func executeProjectExec(r Request, stdout, stderr io.Writer, input ...*os.File) int {
	var args []string
	code := manageOperation(r, stderr, func(ctx context.Context) (int, error) {
		call, err := projectCall(stderr, false)
		if err != nil {
			return 125, err
		}
		args, err = projectExecArguments(ctx, r, newProjectBackend(r.Reference, call))
		return 0, err
	})
	if code != 0 {
		return code
	}
	out, ok := stdout.(*os.File)
	if !ok {
		return 125
	}
	diagnostic, ok := stderr.(*os.File)
	if !ok {
		return 125
	}
	in := os.Stdin
	if len(input) > 0 {
		in = input[0]
	}
	// Let the ordinary exec path own terminal state, signals and command status.
	return Execute(args, in, out, diagnostic)
}
