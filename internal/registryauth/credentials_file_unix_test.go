//go:build linux || darwin

package registryauth

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

	"golang.org/x/sys/unix"
)

func TestCredentialInheritedInputLifetime(t *testing.T) {
	const modeEnvironment = "CASKLET_TEST_CREDENTIAL_INHERITED_INPUT"
	if mode := os.Getenv(modeEnvironment); mode != "" {
		before := credentialInputFlags(t, os.Stdin)
		if before&unix.O_NONBLOCK != 0 {
			t.Fatal("subprocess stdin must be a blocking inherited descriptor")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		want := context.Canceled
		if mode == "deadline" {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			want = context.DeadlineExceeded
		} else if mode == "cancel" {
			timer := time.AfterFunc(150*time.Millisecond, cancel)
			defer timer.Stop()
		}
		credential, err := DecodeContext(ctx, os.Stdin, "ghcr.io")
		if mode == "success" {
			if err != nil || credential != (Credential{Registry: "ghcr.io"}) {
				t.Fatalf("credential handshake: %+v, %v", credential, err)
			}
		} else if !errors.Is(err, want) {
			t.Fatalf("credential cancellation: %v", err)
		}
		if after := credentialInputFlags(t, os.Stdin); after != before {
			t.Fatalf("caller input flags changed: %d => %d", before, after)
		}
		if _, err := fmt.Fprintln(os.Stdout, "decode-finished"); err != nil {
			t.Fatal(err)
		}
		if mode != "success" {
			data := make([]byte, len("caller-input\n"))
			if _, err := io.ReadFull(os.Stdin, data); err != nil || string(data) != "caller-input\n" {
				t.Fatalf("caller input closed or still consumed by the decoder: %q, %v", data, err)
			}
		}
		return
	}
	for _, mode := range []string{"deadline", "cancel", "success"} {
		t.Run(mode, func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close() }()
			defer func() { _ = writer.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCredentialInheritedInputLifetime$")
			cmd.Env = append(os.Environ(), modeEnvironment+"="+mode)
			cmd.Stdin = input
			cmd.Stderr = os.Stderr
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			if mode == "success" {
				if err := json.NewEncoder(writer).Encode(Credential{Registry: "ghcr.io"}); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || line != "decode-finished\n" {
				t.Fatalf("inherited credential input did not finish: %q, %v, %v", line, err, ctx.Err())
			}
			if mode != "success" {
				if _, err := io.WriteString(writer, "caller-input\n"); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.Wait(); err != nil || ctx.Err() != nil {
				t.Fatalf("inherited credential input: %v, %v", err, ctx.Err())
			}
		})
	}
}

func credentialInputFlags(t *testing.T, file *os.File) int {
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
