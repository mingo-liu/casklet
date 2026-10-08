package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestHostPortCheckRejectsInvalidResponses(t *testing.T) {
	ports := []config.PortMapping{{HostIP: "127.0.0.3", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}}
	for _, test := range []struct{ name, response, want string }{
		{"allowed", `{"version":1,"allowed":true}` + "\n", ""},
		{"denied", `{"version":1,"allowed":false,"error":"occupied"}` + "\n", "occupied"},
		{"missing version", `{"allowed":true}` + "\n", "invalid"},
		{"contradiction", `{"version":1,"allowed":true,"error":"occupied"}` + "\n", "invalid"},
		{"unknown field", `{"version":1,"allowed":true,"extra":0}` + "\n", "decode"},
		{"trailing data", `{"version":1,"allowed":true} {}` + "\n", "trailing"},
		{"disconnect", "", "read"},
		{"unterminated", `{"version":1,"allowed":true}`, "read"},
		{"oversized", strings.Repeat(" ", PortCheckLimit) + "{}\n", "size limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := CheckHostPorts(context.Background(), strings.NewReader(test.response), &output, ports)
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("response error: %v; want %q", err, test.want)
			}
			var request PortCheckRequest
			if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Version != 1 || request.Kind != "port-check" || len(request.Publish) != 1 || request.Publish[0] != ports[0] {
				t.Fatalf("request: %+v %v", request, err)
			}
		})
	}
}

func TestHostPortCheckCancellationAndNoPorts(t *testing.T) {
	var output bytes.Buffer
	if err := CheckHostPorts(context.Background(), nil, &output, nil); err != nil || output.Len() != 0 {
		t.Fatalf("unnecessary handshake: %s %v", output.String(), err)
	}
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := CheckHostPorts(ctx, input, &output, []config.PortMapping{{HostIP: "127.0.0.3", HostPort: 8080, ContainerPort: 80, Protocol: "tcp"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
}
