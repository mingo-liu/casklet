package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/project"
)

type boundedProjectOutput struct {
	data     []byte
	overflow bool
	limit    int
}

func (output *boundedProjectOutput) Write(data []byte) (int, error) {
	remaining := output.limit - len(output.data)
	if len(data) > remaining {
		output.overflow = true
	}
	output.data = append(output.data, data[:min(len(data), remaining)]...)
	return len(data), nil
}

const projectPreviewEnvironment = "CASKLET_PROJECT_PREVIEW"

func projectCall(diagnostic io.Writer, preview bool) (project.Call, error) {
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, args ...string) ([]byte, error) {
		output := &boundedProjectOutput{limit: 8 << 20}
		cmd := exec.CommandContext(ctx, binary, args...)
		if preview {
			cmd.Env = append(os.Environ(), projectPreviewEnvironment+"=1")
		}
		// Allow each existing CLI to cancel its remote session and detached startup
		// normally before the bounded fallback kills an unresponsive child.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 80 * time.Second
		cmd.Stdout, cmd.Stderr = output, diagnostic
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("project operation %s failed: %w", args[0], err)
		}
		if output.overflow {
			return nil, errors.New("project operation output exceeds 8 MiB")
		}
		return output.data, nil
	}, nil
}

func executeProject(r Request, stdout, stderr io.Writer) int {
	return manageOperation(r, stderr, func(ctx context.Context) (int, error) {
		return executeProjectContext(ctx, r, stdout, stderr)
	})
}

func executeProjectContext(ctx context.Context, r Request, stdout, stderr io.Writer) (int, error) {
	parent := ctx
	var deadline time.Time
	if r.Action == "project-up" && r.Wait {
		deadline = time.Now().Add(r.WaitTimeout)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	preparationError := func(err error) error {
		if err != nil && !deadline.IsZero() && parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.Join(project.ErrWaitTimeout, err)
		}
		return err
	}
	name := r.Reference
	if r.Action == "project-up" {
		name = r.Project.Name
	}
	call, err := projectCall(stderr, r.DryRun)
	if err != nil {
		return 125, preparationError(err)
	}
	// Read-only status must remain available while up waits for a dependency.
	if r.Action == "project-ps" {
		runner := project.Runner{Call: call}
		records, err := runner.Records(ctx)
		if err != nil {
			return 125, preparationError(err)
		}
		return 0, writeRecords(stdout, project.Managed(records, name), r.JSON)
	}
	root, err := project.DefaultRoot()
	if err != nil {
		return 125, preparationError(err)
	}
	store, err := project.Open(ctx, root, name)
	if err != nil {
		return 125, preparationError(err)
	}
	defer store.Close()
	state, err := store.Load()
	if err != nil {
		return 125, preparationError(err)
	}
	runner := project.Runner{Call: call, Save: store.Save, State: state}
	switch r.Action {
	case "project-up":
		records, err := runner.Up(parent, r.Project, project.UpOptions{Timeout: r.WaitTimeout, Wait: r.Wait, Deadline: deadline, Recreate: r.Recreate, DryRun: r.DryRun})
		if err != nil {
			return 125, err
		}
		if r.DryRun {
			if r.JSON {
				return 0, json.NewEncoder(stdout).Encode(runner.Preview)
			}
			for _, change := range runner.Preview {
				details := []string{change.Reason}
				if len(change.ChangedFields) > 0 {
					details = append(details, "changed fields: "+strings.Join(change.ChangedFields, ", "))
				}
				if change.DetailsUnavailable {
					details = append(details, "field details unavailable")
				}
				if change.RequiresRecreate {
					details = append(details, "requires --recreate "+change.Name)
				}
				if _, err := fmt.Fprintf(stdout, "%s %s %s (%s)\n", change.Action, change.Kind, change.Name, strings.Join(details, "; ")); err != nil {
					return 125, err
				}
			}
			return 0, nil
		}
		return 0, writeRecords(stdout, records, r.JSON)
	case "project-down":
		return 0, runner.Down(ctx, name, r.RemoveVolumes)
	}
	return 125, errors.New("invalid project operation")
}
