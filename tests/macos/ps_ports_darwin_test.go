//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/project"
)

func TestPSShowsPublishedMacPorts(t *testing.T) {
	client(t)
	tcp, udp := freePort(t), freePort(t)
	label := fmt.Sprintf("ps-ports=%d", tcp)
	name := fmt.Sprintf("ps-ports-%d", tcp)
	id := detached(t, "--label", label, "--network", "bridge",
		"--label", project.LabelName+"="+name, "--label", project.LabelService+"=server", "--label", project.LabelSpec+"=ports-test",
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", tcp),
		"-p", fmt.Sprintf("%d:5353/udp", udp), "--", "/bin/sleep", "300")
	want := []config.PortMapping{
		{HostIP: "127.0.0.1", HostPort: uint16(tcp), ContainerPort: 8080, Protocol: "tcp"},
		{HostIP: "0.0.0.0", HostPort: uint16(udp), ContainerPort: 5353, Protocol: "udp"},
	}
	check := func(all bool) {
		t.Helper()
		args := []string{"ps", "--filter", "label=" + label}
		if all {
			args = append(args, "-a")
		}
		out := success(t, args...)
		ports := fmt.Sprintf("127.0.0.1:%d->8080/tcp, 0.0.0.0:%d->5353/udp", tcp, udp)
		if !strings.Contains(out, "PORTS") || !strings.Contains(out, id[:12]) || !strings.Contains(out, ports) || strings.Contains(out, "127.0.0.2:") || strings.Contains(out, "127.0.0.3:") {
			t.Fatalf("Mac table: %s", out)
		}
		var records []container.Record
		if err := json.Unmarshal([]byte(success(t, append(args, "--json")...)), &records); err != nil || len(records) != 1 || records[0].ID != id || !reflect.DeepEqual(records[0].Publish, want) {
			t.Fatalf("Mac JSON: %+v %v", records, err)
		}
		if out := success(t, "project", "ps", name); !strings.Contains(out, ports) {
			t.Fatalf("project table: %s", out)
		}
		if err := json.Unmarshal([]byte(success(t, "project", "ps", "--json", name)), &records); err != nil || len(records) != 1 || records[0].ID != id || !reflect.DeepEqual(records[0].Publish, want) {
			t.Fatalf("project JSON: %+v %v", records, err)
		}
	}
	check(false)
	success(t, "stop", "--timeout", "0s", id)
	check(true)
	if out := success(t, "ps", "--filter", "label="+label); strings.Contains(out, id[:12]) {
		t.Fatalf("default listing includes stopped container: %s", out)
	}
}
