//go:build linux || darwin

package cli

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// exec inherits blocking descriptors even when the parent's os.Pipe files are
// pollable. This exercises the actual command boundary, not a local pipe copy.
func TestArchiveInheritedPipeCancellation(t *testing.T) {
	const modeEnvironment = "CASKLET_TEST_INHERITED_ARCHIVE_PIPE"
	if mode := os.Getenv(modeEnvironment); mode != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		file := os.Stdin
		if mode == "write" {
			file = os.Stdout
		}
		if mode == "write" {
			// Darwin sets the read-only FWASWRITTEN history bit on the first write.
			// Prime that history before capturing flags so equality tests configuration
			// restoration rather than asking F_SETFL to undo an ordinary write.
			if _, err := file.Write([]byte{0}); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		raw, err := file.SyscallConn()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		before := -1
		var statusErr error
		if err := raw.Control(func(fd uintptr) { before, statusErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || statusErr != nil {
			fmt.Fprintf(os.Stderr, "original status: %v %v\n", err, statusErr)
			os.Exit(1)
		}
		stream, finish, err := cancelableStreamFile(ctx, file)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if mode == "write" {
			_, err = stream.Write(make([]byte, 1<<20))
		} else {
			_, err = stream.Read(make([]byte, 32))
		}
		finish()
		after := -1
		if err := raw.Control(func(fd uintptr) { after, statusErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || statusErr != nil || before != after {
			fmt.Fprintf(os.Stderr, "original file closed or flags changed: %d => %d, %v %v\n", before, after, err, statusErr)
			os.Exit(1)
		}

		if ctx.Err() == nil || err == nil {
			fmt.Fprintf(os.Stderr, "blocked %s did not cancel: %v\n", mode, err)
			os.Exit(1)
		}
		// Avoid the test runner writing its PASS line to the deliberately full pipe.
		os.Exit(0)
	}
	for _, mode := range []string{"read", "write"} {
		t.Run(mode, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestArchiveInheritedPipeCancellation$")
			cmd.Env = append(os.Environ(), modeEnvironment+"="+mode)
			if mode == "read" {
				cmd.Stdin = reader
			} else {
				cmd.Stdout = writer
			}
			var diagnostic strings.Builder
			cmd.Stderr = &diagnostic
			if err := cmd.Run(); err != nil || ctx.Err() != nil {
				t.Fatalf("inherited %s cancellation: %v, %v, %s", mode, err, ctx.Err(), diagnostic.String())
			}
		})
	}
}

func TestArchiveFileSetupRestoresStatusWithoutIO(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	for _, file := range []*os.File{reader, writer} {
		raw, err := file.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		flags := func() int {
			t.Helper()
			value := -1
			var statErr error
			err := raw.Control(func(fd uintptr) { value, statErr = unix.FcntlInt(fd, unix.F_GETFL, 0) })
			if err != nil || statErr != nil {
				t.Fatalf("original descriptor: %v %v", err, statErr)
			}
			return value
		}
		before := flags()
		_, finish, err := cancelableStreamFile(context.Background(), file)
		if err != nil {
			t.Fatal(err)
		}
		finish()
		if after := flags(); after != before {
			t.Fatalf("status flags changed without I/O: %d => %d", before, after)
		}
		if _, err := file.Stat(); err != nil {
			t.Fatalf("caller file closed: %v", err)
		}
	}
}
