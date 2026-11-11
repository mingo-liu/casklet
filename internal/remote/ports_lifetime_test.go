package remote

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestHostPortCheckCancellationClosesInput(t *testing.T) {
	var (
		input, writer = io.Pipe()
		ctx, cancel   = context.WithTimeout(context.Background(), 20*time.Millisecond)
	)
	defer func() { _ = input.Close() }()
	defer func() { _ = writer.Close() }()
	defer cancel()
	ports := []config.PortMapping{{HostIP: "127.0.0.3", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	if err := CheckHostPorts(ctx, input, io.Discard, ports); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	written := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("late response\n"))
		written <- err
	}()
	select {
	case err := <-written:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("canceled handshake input remains open: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled handshake retained its input reader")
	}
}
