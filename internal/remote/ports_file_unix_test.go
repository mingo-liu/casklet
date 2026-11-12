//go:build linux || darwin

package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"golang.org/x/sys/unix"
)

func TestHostPortInheritedInputLifetime(t *testing.T) {
	const modeEnvironment = "CASKLET_TEST_HOST_PORT_INHERITED_INPUT"
	if mode := os.Getenv(modeEnvironment); mode != "" {
		before := portInputFlags(t, os.Stdin)
		if before&unix.O_NONBLOCK != 0 {
			t.Fatal("subprocess stdin must be a blocking inherited descriptor")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		ports := []config.PortMapping{{HostIP: "127.0.0.3", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
		err := CheckHostPorts(ctx, os.Stdin, os.Stdout, ports)
		if mode == "cancel" && !errors.Is(err, context.DeadlineExceeded) || mode == "allow" && err != nil {
			t.Fatalf("host port check: %v", err)
		}
		if after := portInputFlags(t, os.Stdin); after != before {
			t.Fatalf("caller input flags changed: %d => %d", before, after)
		}
		if _, err := fmt.Fprintln(os.Stderr, "check-finished"); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, len("caller-input\n"))
		if _, err := io.ReadFull(os.Stdin, data); err != nil || string(data) != "caller-input\n" {
			t.Fatalf("caller input closed or still consumed by the handshake: %q, %v", data, err)
		}
		return
	}
	for _, mode := range []string{"cancel", "allow"} {
		t.Run(mode, func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close() }()
			defer func() { _ = writer.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHostPortInheritedInputLifetime$")
			cmd.Env = append(os.Environ(), modeEnvironment+"="+mode)
			cmd.Stdin = input
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			diagnostic, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			line, err := bufio.NewReader(output).ReadBytes('\n')
			var request PortCheckRequest
			if err != nil || DecodePortCheck(line, &request) != nil || request.Version != 1 || request.Kind != "port-check" || len(request.Publish) != 1 {
				t.Fatalf("port check request: %q, %v", line, err)
			}
			if mode == "allow" {
				if err := json.NewEncoder(writer).Encode(PortCheckResult{Version: 1, Allowed: true}); err != nil {
					t.Fatal(err)
				}
			}
			ready, err := bufio.NewReader(diagnostic).ReadString('\n')
			if err != nil || ready != "check-finished\n" {
				t.Fatalf("inherited input did not finish its handshake: %q, %v, %v", ready, err, ctx.Err())
			}
			if _, err := io.WriteString(writer, "caller-input\n"); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil || ctx.Err() != nil {
				t.Fatalf("inherited input check: %v, %v", err, ctx.Err())
			}
		})
	}
}

func TestHostPortRegularFileInputRetainsOwnership(t *testing.T) {
	input, err := os.CreateTemp(t.TempDir(), "port-check")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	if _, err := io.WriteString(input, "{\"version\":1,\"allowed\":true}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	before := portInputFlags(t, input)
	ports := []config.PortMapping{{HostIP: "127.0.0.3", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	if err := CheckHostPorts(context.Background(), input, io.Discard, ports); err != nil {
		t.Fatal(err)
	}
	if after := portInputFlags(t, input); after != before {
		t.Fatalf("caller input flags changed: %d => %d", before, after)
	}
	if _, err := input.Stat(); err != nil {
		t.Fatalf("caller input was closed: %v", err)
	}
}

func portInputFlags(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	flags := -1
	var statusErr error
	if err := raw.Control(func(fd uintptr) { flags, statusErr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil || statusErr != nil {
		t.Fatalf("caller input descriptor: %v, %v", err, statusErr)
	}
	return flags
}
