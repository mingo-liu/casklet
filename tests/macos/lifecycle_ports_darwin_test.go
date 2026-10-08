//go:build darwin

package macos

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func listenMacPort(protocol, address string) (io.Closer, int, error) {
	if protocol == "udp" {
		listener, err := net.ListenPacket("udp4", address)
		if err != nil {
			return nil, 0, err
		}
		return listener, listener.LocalAddr().(*net.UDPAddr).Port, nil
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return nil, 0, err
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

func TestLifecycleRejectsOccupiedMacPorts(t *testing.T) {
	for _, test := range []struct{ action, protocol, host string }{
		{"start", "tcp", "0.0.0.0"},
		{"start", "udp", "127.0.0.1"},
		{"restart", "tcp", "127.0.0.1"},
		{"restart", "udp", "0.0.0.0"},
	} {
		t.Run(test.action+"-"+test.protocol+"-"+test.host, func(t *testing.T) {
			client(t)
			listener, port, err := listenMacPort(test.protocol, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listener.Close()
			id := detached(t, "--network", "bridge", "-p", fmt.Sprintf("%s:%d:8080/%s", test.host, port, test.protocol), "--", "/bin/sleep", "300")
			// A running start is idempotent even while Lima owns its host sockets.
			original := inspectHost(t, id)
			success(t, "start", id)
			if next := inspectHost(t, id); next.Generation != original.Generation {
				t.Fatalf("idempotent start changed generation: %+v", next)
			}
			success(t, "stop", "--timeout", "0s", id)
			stopped := inspectHost(t, id)
			address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			deadline := time.Now().Add(10 * time.Second)
			for {
				listener, _, err = listenMacPort(test.protocol, address)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("Lima did not release %s: %v", address, err)
				}
				time.Sleep(50 * time.Millisecond)
			}
			defer listener.Close()
			out, diagnostic, code := command(t, test.action, id)
			if code != 125 || out != "" || !strings.Contains(diagnostic, "macOS host port unavailable") {
				t.Fatalf("occupied port: exit=%d stdout=%q stderr=%q", code, out, diagnostic)
			}
			if next := inspectHost(t, id); next.Generation != stopped.Generation || next.State != stopped.State {
				t.Fatalf("failed preflight changed execution: before=%+v after=%+v", stopped, next)
			}
			listener.Close()
			success(t, test.action, id)
			if next := inspectHost(t, id); next.Generation != stopped.Generation+1 || next.State != "running" {
				t.Fatalf("retry did not start a new execution: %+v", next)
			}
			// An active restart must wait for its own old forwarding sockets.
			success(t, "restart", "--timeout", "0s", id)
			if next := inspectHost(t, id); next.Generation != stopped.Generation+2 || next.State != "running" {
				t.Fatalf("active restart did not complete: %+v", next)
			}
		})
	}
}
