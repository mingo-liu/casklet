package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func logRecord(id, key string) container.Record {
	return container.Record{ID: id, Labels: map[string]string{project.LabelName: "demo", project.LabelService: key, project.LabelSpec: "spec"}}
}

func TestProjectLogsSelectionAndArguments(t *testing.T) {
	records := []container.Record{logRecord("one", "api"), logRecord("two", "db")}
	selected, err := selectProjectLogs(records, "demo", []string{"db", "db"})
	if err != nil || len(selected) != 1 || selected[0].ID != "two" {
		t.Fatalf("selection: %v %v", selected, err)
	}
	if _, err := selectProjectLogs(records, "demo", []string{"absent"}); err == nil {
		t.Fatal("missing service accepted")
	}
	if _, err := selectProjectLogs(append(records, logRecord("three", "api")), "demo", nil); err == nil {
		t.Fatal("duplicate service accepted")
	}
	if got, err := selectProjectLogs(records, "other", nil); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	for _, args := range [][]string{{"project", "logs", "--tail", "-1", "demo"}, {"project", "logs", "--tail", "1000001", "demo"}, {"project", "logs", "--service", "../api", "demo"}, {"project", "logs", "demo", "--follow"}, {"project", "logs"}} {
		if _, err := Parse(args); err == nil || !strings.Contains(err.Error(), "project logs --help") {
			t.Fatalf("args %v: %v", args, err)
		}
	}
	r, err := Parse([]string{"project", "logs", "--service", "api", "--tail", "0", "-f", "demo"})
	if err != nil || !r.Follow || r.Tail != 0 || len(r.Services) != 1 {
		t.Fatalf("parse: %+v %v", r, err)
	}
}

func TestProjectLogsPrefixesFragmentedConcurrentOutput(t *testing.T) {
	var out bytes.Buffer
	records := []container.Record{logRecord("one", "api"), logRecord("two", "db")}
	err := streamProjectLogs(context.Background(), records, &out, io.Discard, func(_ context.Context, id string, w, d io.Writer) error {
		for _, part := range []string{"hel", "lo\nsecond", "\nlast"} {
			if _, err := io.WriteString(w, part); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"api", "db"} {
		for _, line := range []string{"hello", "second", "last"} {
			if strings.Count(out.String(), "["+key+"] "+line+"\n") != 1 {
				t.Fatalf("output: %q", out.String())
			}
		}
	}
	var long bytes.Buffer
	w := projectLineWriter{out: &long, key: "api"}
	if _, err := io.WriteString(&w, strings.Repeat("x", 100000)); err != nil {
		t.Fatal(err)
	}
	if len(w.pending) > 32<<10 {
		t.Fatal("unbounded buffer")
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(long.String(), "[api] ") != 4 || strings.Count(long.String(), "x") != 100000 {
		t.Fatal("long output lost")
	}
}

type failedLogOutput struct{}

func (failedLogOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestProjectLogsFailureCancelsAndJoinsReaders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := make(chan struct{})
	finished := make(chan struct{})
	err := streamProjectLogs(ctx, []container.Record{logRecord("one", "api"), logRecord("two", "db")}, failedLogOutput{}, io.Discard, func(ctx context.Context, id string, w, d io.Writer) error {
		if id == "two" {
			close(started)
			<-ctx.Done()
			close(finished)
			return ctx.Err()
		}
		<-started
		_, err := io.WriteString(w, "hello\n")
		return err
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("reader not joined")
	}
}
