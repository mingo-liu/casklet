//go:build linux

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/container"
	"github.com/mingo-liu/mini-docker/internal/remote"
)

func TestHostLifecyclePreflightFencesGenerationAndOperations(t *testing.T) {
	for _, action := range []string{"start", "restart"} {
		for _, outcome := range []string{"deny", "disconnect", "interrupt", "allow"} {
			t.Run(action+"-"+outcome, func(t *testing.T) {
				port := freeNetworkPort(t, "tcp")
				id := startBackground(t, backgroundName(t), []string{"--network", "bridge", "-p", fmt.Sprintf("127.0.0.3:%d:8080", port)}, "/bin/sleep", "300")
				if action == "start" {
					backgroundSuccess(t, "stop", "--timeout", "0s", id)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				args := []string{"__host-lifecycle", action}
				if action == "restart" {
					args = append(args, "--timeout", "0s")
				}
				cmd := exec.CommandContext(ctx, binary, append(args, id)...)
				input, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
				output, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var diagnostic bytes.Buffer
				cmd.Stderr = &diagnostic
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer cmd.Process.Kill()
				reader := bufio.NewReader(output)
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Fatalf("preflight request: %v", err)
				}
				var request remote.PortCheckRequest
				if err := remote.DecodePortCheck(line, &request); err != nil || request.Version != 1 || request.Kind != "port-check" || len(request.Publish) != 1 || int(request.Publish[0].HostPort) != port {
					t.Fatalf("preflight: %+v %v", request, err)
				}
				store, err := container.OpenStore()
				if err != nil {
					t.Fatal(err)
				}
				stopped, err := store.Get(ctx, id)
				if err != nil || !stopped.Terminal() || stopped.Generation != 0 {
					t.Fatalf("preflight did not stop old execution: %+v %v", stopped, err)
				}
				contender, stopContender := context.WithTimeout(ctx, 30*time.Millisecond)
				lock, err := store.AcquireOperation(contender, id)
				stopContender()
				if lock != nil {
					lock.Close()
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("preflight released its operation lock: %v", err)
				}
				expected := 125
				err = nil
				switch outcome {
				case "deny":
					err = json.NewEncoder(input).Encode(remote.PortCheckResult{Version: 1, Error: "occupied Mac port"})
				case "allow":
					err = json.NewEncoder(input).Encode(remote.PortCheckResult{Version: 1, Allowed: true})
					expected = 0
				case "interrupt":
					err = cmd.Process.Signal(syscall.SIGTERM)
					expected = 143
				}
				if err != nil {
					t.Fatal(err)
				}
				if outcome != "interrupt" {
					input.Close()
				}
				data, readErr := io.ReadAll(reader)
				err = cmd.Wait()
				code := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatal(err)
					}
					code = exit.ExitCode()
				}
				if ctx.Err() != nil || readErr != nil || code != expected {
					t.Fatalf("result: exit=%d want=%d read=%v ctx=%v output=%q stderr=%q", code, expected, readErr, ctx.Err(), data, diagnostic.String())
				}
				latest, err := store.Get(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "allow" {
					if string(data) != id+"\n" || latest.Generation != 1 || latest.State != "running" {
						t.Fatalf("authorized startup: output=%q record=%+v", data, latest)
					}
				} else {
					if len(data) != 0 || latest.Generation != stopped.Generation || latest.State != stopped.State {
						t.Fatalf("rejected preflight changed execution: output=%q before=%+v after=%+v", data, stopped, latest)
					}
					if outcome == "deny" && !strings.Contains(diagnostic.String(), "occupied Mac port") {
						t.Fatalf("lost host diagnostic: %q", diagnostic.String())
					}
					// A failed check must release the operation lock for retry.
					backgroundSuccess(t, "start", id)
				}
			})
		}
	}
}
