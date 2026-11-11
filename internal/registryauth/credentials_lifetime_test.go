package registryauth

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type credentialHandshakeReader struct {
	started  chan struct{}
	closed   chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func TestCredentialCancellationDoesNotWaitForUninterruptibleReader(t *testing.T) {
	var (
		ctx, cancel = context.WithCancel(context.Background())
		reader      = &credentialHandshakeReader{
			started:  make(chan struct{}),
			closed:   make(chan struct{}),
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
			t.Error("released credential reader did not finish")
		}
	}()
	go func() {
		_, err := DecodeContext(ctx, reader, "ghcr.io")
		done <- err
	}()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("credential input read did not start")
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
	select {
	case <-reader.closed:
	default:
		t.Fatal("credential cancellation did not close its input")
	}
}

func (r *credentialHandshakeReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	close(r.finished)
	return 0, io.ErrClosedPipe
}

func (r *credentialHandshakeReader) Close() error {
	close(r.closed)
	return nil
}
