//go:build linux || darwin

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestProjectLogsInheritedOutputCancellation(t *testing.T) {
	const modeEnvironment = "CASKLET_TEST_PROJECT_LOG_PIPE"
	if mode := os.Getenv(modeEnvironment); mode != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		out, diagnostic := io.Writer(os.Stdout), io.Writer(io.Discard)
		if mode == "diagnostic" {
			out, diagnostic = io.Discard, os.Stderr
		}
		err := streamProjectLogs(ctx, []container.Record{logRecord("one", "api"), logRecord("two", "db")}, out, diagnostic, func(ctx context.Context, id string, w, d io.Writer) error {
			if id == "two" {
				<-ctx.Done()
				return ctx.Err()
			}
			if mode == "diagnostic" {
				_, err := io.WriteString(d, strings.Repeat("diagnostic\n", 1<<17))
				return err
			}
			_, err := io.WriteString(w, strings.Repeat("log line\n", 1<<17))
			return err
		})
		if err == nil || ctx.Err() == nil {
			os.Exit(1)
		}
		// The inherited output pipe is deliberately full, including for test output.
		os.Exit(0)
	}
	for _, mode := range []string{"stdout", "diagnostic"} {
		t.Run(mode, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProjectLogsInheritedOutputCancellation$")
			cmd.Env = append(os.Environ(), modeEnvironment+"="+mode)
			if mode == "diagnostic" {
				cmd.Stderr = writer
			} else {
				cmd.Stdout = writer
			}
			if err := cmd.Run(); err != nil || ctx.Err() != nil {
				t.Fatal(fmt.Errorf("inherited %s cancellation: %v, %v", mode, err, ctx.Err()))
			}
		})
	}
}
