//go:build linux

package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLaunchAutomaticScope(t *testing.T) {
	require(t)
	tests := []struct {
		name, input, output string
		options, command    []string
		code                int
	}{
		{
			name: "streams arguments and exit status", input: "piped input\n",
			command: []string{"/bin/sh", "-c", "cat; printf '%s\\n' \"$1\" \"$2\"; echo diagnostic >&2; exit 7", "shell", "two words", "$(literal)"},
			code:    7, output: "piped input\ntwo words\n$(literal)\n",
		},
		{
			name: "timeout", options: []string{"--timeout", "100ms", "--stop-timeout", "0s"},
			command: []string{"/bin/sleep", "10"}, code: 124,
		},
		{
			name: "delegation", command: []string{"/bin/cat", "/proc/self/cgroup"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			args := append([]string{"run", "--rootfs", _template}, tt.options...)
			args = append(args, "--")
			args = append(args, tt.command...)
			cmd := exec.CommandContext(ctx, _binary, args...)
			cmd.Env = append(os.Environ(), "CASKLET_SCOPE_LAUNCHED=")
			cmd.Stdin = strings.NewReader(tt.input)
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if ctx.Err() != nil || code != tt.code {
				t.Fatalf("exit=%d error=%v stdout=%q stderr=%q", code, ctx.Err(), stdout.String(), stderr.String())
			}
			if tt.name == "delegation" {
				if !strings.Contains(stdout.String(), ".scope/container-") {
					t.Fatalf("workload is outside an automatic delegated scope: %q", stdout.String())
				}
			} else if stdout.String() != tt.output {
				t.Fatalf("stdout=%q; want %q", stdout.String(), tt.output)
			}
			if tt.code == 7 && stderr.String() != "diagnostic\n" {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
	backgroundSuccess(t, "doctor", "--rootfs", _template)
}

func TestLaunchScopeFailureDoesNotRecurse(t *testing.T) {
	require(t)
	t.Setenv("CASKLET_SCOPE_LAUNCHED", "1")
	code, out, stderr := backgroundCLI(t, "doctor", "--rootfs", _template)
	if code != 125 || out != "" || !strings.Contains(stderr, "automatic cgroup delegation failed:") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
}
