package remote

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

type portHandshakeReader struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func TestHostPortCancellationDoesNotWaitForUninterruptibleReader(t *testing.T) {
	var (
		ctx, cancel = context.WithCancel(context.Background())
		reader      = &portHandshakeReader{
			started:  make(chan struct{}),
			release:  make(chan struct{}),
			finished: make(chan struct{}),
		}
		done = make(chan error, 1)
	)
	defer cancel()
	defer func() {
		close(reader.release)
		select {
		case <-reader.finished:
		case <-time.After(time.Second):
			t.Error("released port reader did not finish")
		}
	}()
	ports := []config.PortMapping{{HostIP: "127.0.0.3", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	go func() { done <- CheckHostPorts(ctx, reader, io.Discard, ports) }()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("port input read did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for a reader that Close cannot interrupt")
	}
}

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

func (r *portHandshakeReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	close(r.finished)
	return 0, io.EOF
}

func (*portHandshakeReader) Close() error { return nil }
