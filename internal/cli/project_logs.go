package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func selectProjectLogs(records []container.Record, name string, services []string) ([]container.Record, error) {
	wanted := map[string]bool{}
	for _, key := range services {
		wanted[key] = true
	}
	seen := map[string]bool{}
	selected := []container.Record{}
	for _, record := range project.Managed(records, name) {
		key := record.Labels[project.LabelService]
		if len(wanted) > 0 && !wanted[key] {
			continue
		}
		if err := project.ValidateName(key); err != nil {
			return nil, errors.New("invalid project service label")
		}
		if seen[key] {
			return nil, fmt.Errorf("duplicate project service %q", key)
		}
		seen[key] = true
		selected = append(selected, record)
	}
	for _, key := range services {
		if !seen[key] {
			return nil, fmt.Errorf("project service %q not found", key)
		}
	}
	if len(selected) > 64 {
		return nil, errors.New("project logs supports at most 64 services")
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].Labels[project.LabelService] < selected[j].Labels[project.LabelService]
	})
	return selected, nil
}

func projectLogCall(ctx context.Context, id string, tail int, follow bool, out, diagnostic io.Writer) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"logs"}
	if tail >= 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, id)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 80 * time.Second
	cmd.Stdout, cmd.Stderr = out, diagnostic
	return cmd.Run()
}

type lockedProjectWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *lockedProjectWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(data)
}

// Each stream retains at most one bounded unfinished line. Only complete
// prefixed lines/chunks hold the shared writer lock, so idle peers cannot block it.
type projectLineWriter struct {
	out     io.Writer
	key     string
	pending []byte
}

func (w *projectLineWriter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	data := append([]byte("["+w.key+"] "), w.pending...)
	if data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	n, err := w.out.Write(data)
	w.pending = w.pending[:0]
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}
func (w *projectLineWriter) Write(data []byte) (int, error) {
	total := len(data)
	for len(data) > 0 {
		n := min(len(data), (32<<10)-len(w.pending))
		if i := bytes.IndexByte(data[:n], '\n'); i >= 0 {
			n = i + 1
		}
		w.pending = append(w.pending, data[:n]...)
		data = data[n:]
		if w.pending[len(w.pending)-1] == '\n' || len(w.pending) == 32<<10 {
			if err := w.flush(); err != nil {
				return total - len(data), err
			}
		}
	}
	return total, nil
}

func streamProjectLogs(ctx context.Context, records []container.Record, out, diagnostic io.Writer, stream func(context.Context, string, io.Writer, io.Writer) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &lockedProjectWriter{out: out}
	diagnostics := &lockedProjectWriter{out: diagnostic}
	var wg sync.WaitGroup
	var once sync.Once
	var failure error
	for _, record := range records {
		wg.Go(func() {
			key := record.Labels[project.LabelService]
			lines := &projectLineWriter{out: output, key: key}
			err := stream(ctx, record.ID, lines, diagnostics)
			err = errors.Join(err, lines.flush())
			if err != nil {
				once.Do(func() { failure = fmt.Errorf("logs for service %q: %w", key, err); cancel() })
			}
		})
	}
	wg.Wait()
	if failure != nil {
		return failure
	}
	return ctx.Err()
}
