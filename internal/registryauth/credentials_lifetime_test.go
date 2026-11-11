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

func TestCredentialCancellationWaitsForReader(t *testing.T) {
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
	defer close(reader.release)
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
	case <-reader.closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close credential input")
	}
	select {
	case err := <-done:
		t.Fatalf("cancellation returned before the input read stopped: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	reader.release <- struct{}{}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled credential input was not joined")
	}
	select {
	case <-reader.finished:
	default:
		t.Fatal("credential input read remains active")
	}
}

func (r *credentialHandshakeReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.closed
	<-r.release
	close(r.finished)
	return 0, io.ErrClosedPipe
}

func (r *credentialHandshakeReader) Close() error {
	close(r.closed)
	return nil
}
