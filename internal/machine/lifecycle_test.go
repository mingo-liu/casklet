package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/remote"
)

func TestHostPortMappingsRejectInvalidGuestResources(t *testing.T) {
	ports := []config.PortMapping{{HostIP: "127.0.0.2", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}, {HostIP: "127.0.0.3", HostPort: 8081, ContainerPort: 80, Protocol: "udp"}}
	translated, err := hostPortMappings(ports)
	if err != nil || translated[0].HostIP != "0.0.0.0" || translated[1].HostIP != "127.0.0.1" || ports[0].HostIP != "127.0.0.2" {
		t.Fatalf("translation: %+v %v", translated, err)
	}
	for _, invalid := range []config.PortMapping{
		{HostIP: "192.0.2.1", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"},
		{HostIP: "127.0.0.3", HostPort: 22, ContainerPort: 80, Protocol: "tcp"},
		{HostIP: "127.0.0.3", HostPort: 8080, ContainerPort: 80, Protocol: "invalid"},
		{HostIP: "127.0.0.3", HostPort: 0, ContainerPort: 80, Protocol: "tcp"},
	} {
		if _, err := hostPortMappings([]config.PortMapping{invalid}); err == nil {
			t.Fatalf("accepted %+v", invalid)
		}
	}
	ports[1].HostPort, ports[1].Protocol = ports[0].HostPort, ports[0].Protocol
	if _, err := hostPortMappings(ports); err == nil {
		t.Fatal("accepted ports that overlap after host translation")
	}
}

func TestLifecyclePortCheckWaitsForReleaseAndHonorsCancellation(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ports := []config.PortMapping{{HostIP: "127.0.0.3", HostPort: uint16(listener.Addr().(*net.TCPAddr).Port), ContainerPort: 80, Protocol: "tcp"}}
	if err := waitHostPorts(context.Background(), ports, 30*time.Millisecond); err == nil || !strings.Contains(err.Error(), "macOS host port unavailable") {
		t.Fatalf("occupied port: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitHostPorts(ctx, ports, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	closed := make(chan struct{})
	timer := time.AfterFunc(75*time.Millisecond, func() { listener.Close(); close(closed) })
	defer timer.Stop()
	if err := waitHostPorts(context.Background(), ports, time.Second); err != nil {
		t.Fatalf("released port: %v", err)
	}
	<-closed
}

type portResponse struct {
	bytes.Buffer
	closed bool
}

func (response *portResponse) Close() error { response.closed = true; return nil }

func TestLifecycleOutputConsumesFragmentedHandshakeAndPreservesID(t *testing.T) {
	var stdout bytes.Buffer
	response := &portResponse{}
	output := &portCheckOutput{ctx: context.Background(), response: response, stdout: &stdout}
	request := `{"version":1,"kind":"port-check","publish":[]}` + "\n"
	id := strings.Repeat("a", 32) + "\n"
	for _, chunk := range []string{request[:5], request[5 : len(request)-1], request[len(request)-1:] + id[:7], id[7:]} {
		if n, err := output.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("write: %d %v", n, err)
		}
	}
	var result remote.PortCheckResult
	if err := json.Unmarshal(response.Bytes(), &result); err != nil || !result.Allowed || result.Version != 1 || !response.closed || stdout.String() != id {
		t.Fatalf("output=%q response=%+v closed=%v err=%v", stdout.String(), result, response.closed, err)
	}
}

func TestLifecycleOutputRejectsMalformedHandshakeAndSupportsNoCheck(t *testing.T) {
	for _, data := range []string{
		`{"version":2,"kind":"port-check","publish":[]}` + "\n",
		`{"version":1,"kind":"other","publish":[]}` + "\n",
		`{"version":1,"kind":"port-check","publish":[],"extra":0}` + "\n",
		strings.Repeat("{", remote.PortCheckLimit+1),
	} {
		response := &portResponse{}
		var stdout bytes.Buffer
		output := &portCheckOutput{ctx: context.Background(), response: response, stdout: &stdout}
		if _, err := output.Write([]byte(data)); err == nil || !response.closed || stdout.Len() != 0 {
			t.Fatalf("accepted malformed handshake: closed=%v output=%q err=%v", response.closed, stdout.String(), err)
		}
	}
	response := &portResponse{}
	var stdout bytes.Buffer
	output := &portCheckOutput{ctx: context.Background(), response: response, stdout: &stdout}
	id := strings.Repeat("b", 32) + "\n"
	if _, err := output.Write([]byte(id)); err != nil || stdout.String() != id || !response.closed || response.Len() != 0 {
		t.Fatalf("no-check output: %q closed=%v err=%v", stdout.String(), response.closed, err)
	}
}
