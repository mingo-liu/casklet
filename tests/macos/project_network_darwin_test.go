//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

func TestProjectNetworkReadinessAndBatchLifecycle(t *testing.T) {
	client(t)
	project := fmt.Sprintf("project-%x", time.Now().UnixNano())
	selector := "label=project=" + project
	success(t, "network", "create", project)
	removed := false
	t.Cleanup(func() {
		if !removed {
			command(t, "network", "rm", project)
		}
	})
	port := freePort(t)
	backend := detached(t, "--network", project, "--network-alias", "redis", "--label", "project="+project,
		"-p", fmt.Sprintf("127.0.0.1:%d:8080", port),
		"--health-cmd", `test "$(wget -T 1 -qO- http://127.0.0.1:8080)" = project-ready`,
		"--health-interval", "100ms", "--health-timeout", "2s", "--health-retries", "1", "--",
		"/bin/sh", "-c", `mkdir -p /srv; printf project-ready > /srv/index.html; exec httpd -f -p 8080 -h /srv`)
	frontend := detached(t, "--network", project, "--label", "project="+project,
		"--health-cmd", `test "$(wget -T 1 -qO- http://redis:8080)" = project-ready`,
		"--health-interval", "100ms", "--health-timeout", "2s", "--health-retries", "1", "--", "/bin/sleep", "300")
	success(t, "wait", "--healthy", "--timeout", "10s", backend)
	success(t, "wait", "--healthy", "--timeout", "10s", frontend)

	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	host := &http.Client{Transport: transport, Timeout: 500 * time.Millisecond}
	assertHostResponse := func() {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			response, err := host.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
				response.Body.Close()
				if readErr == nil && response.StatusCode == http.StatusOK && string(body) == "project-ready" {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("named-network published HTTP port did not become reachable on the Mac")
	}
	assertHostResponse()
	address := func(id string) string {
		t.Helper()
		return strings.TrimSpace(success(t, "exec", id, "--", "/bin/sh", "-c", `ip -4 addr show dev eth0 | awk '$1 == "inet" {print $2}'`))
	}
	oldAddress := address(backend)
	if oldAddress == "" {
		t.Fatal("backend has no network address")
	}
	success(t, "stop", "--timeout", "0s", backend)
	macHealth(t, frontend, container.HealthUnhealthy)
	// Hold the released address so restarting must publish a different DNS answer.
	detached(t, "--network", project, "--label", "project="+project, "--", "/bin/sleep", "300")
	success(t, "start", backend)
	if current := address(backend); current == "" || current == oldAddress {
		t.Fatalf("backend address was not replaced: before=%q after=%q", oldAddress, current)
	}
	success(t, "wait", "--healthy", "--timeout", "10s", frontend)
	assertHostResponse()
	var healthy []container.Record
	if err := json.Unmarshal([]byte(success(t, "ps", "--json", "--filter", selector, "--filter", "health=healthy")), &healthy); err != nil || len(healthy) != 2 {
		t.Fatalf("project health filter: records=%v error=%v", healthy, err)
	}
	success(t, "stop", "--timeout", "0s", "--filter", selector)
	success(t, "rm", "--filter", selector)
	success(t, "network", "rm", project)
	removed = true
}
