//go:build linux

package runtime

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestControlEventsStopInterruptsBlockedRead(t *testing.T) {
	control, _ := controlTestPair(t)
	events, stop := startControlEvents(control)
	t.Cleanup(stop)
	assertControlReaderStops(t, stop)
	if _, ok := <-events; ok {
		t.Fatal("reader published an event without input")
	}
}

func TestControlEventsStopInterruptsFullQueue(t *testing.T) {
	control, writer := controlTestPair(t)
	events, stop := startControlEvents(control)
	t.Cleanup(stop)
	encoder := json.NewEncoder(writer)
	for i := 0; i < cap(events)+1; i++ {
		if err := encoder.Encode(message{Kind: "started"}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for len(events) != cap(events) {
		if time.Now().After(deadline) {
			t.Fatal("reader did not fill its event queue")
		}
		time.Sleep(time.Millisecond)
	}
	assertControlReaderStops(t, stop)
	for range events {
	}
}

func TestControlEventsPreserveProtocolOrder(t *testing.T) {
	control, writer := controlTestPair(t)
	events, stop := startControlEvents(control)
	t.Cleanup(stop)
	encoder := json.NewEncoder(writer)
	for _, kind := range []string{"ready", "started", "exited"} {
		if err := encoder.Encode(message{Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"ready", "started", "exited"} {
		select {
		case event := <-events:
			if event.Kind != want {
				t.Fatalf("event = %q, want %q", event.Kind, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing event %q", want)
		}
	}
}

func controlTestPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	control := os.NewFile(uintptr(fds[0]), "test-control")
	writer := os.NewFile(uintptr(fds[1]), "test-control-peer")
	t.Cleanup(func() {
		_ = control.Close()
		_ = writer.Close()
	})
	return control, writer
}

func assertControlReaderStops(t *testing.T, stop func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		stop()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("control reader did not stop")
	}
}
