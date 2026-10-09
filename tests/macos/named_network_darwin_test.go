//go:build darwin

package macos

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNamedNetworkServiceDiscoveryAndHostPublish(t *testing.T) {
	name := fmt.Sprintf("mac-network-%d", time.Now().UnixNano())
	success(t, "network", "create", name)
	t.Cleanup(func() { command(t, "network", "rm", name) })
	port := freePort(t)
	server := detached(t, "-p", fmt.Sprintf("127.0.0.1:%d:8080", port), "--name", name+"-server", "--network", name, "--network-alias", "web", "--", "/bin/sh", "-c", "mkdir -p /tmp/web; echo named-network-ok > /tmp/web/index.html; httpd -f -p 8080 -h /tmp/web")
	consumer := detached(t, "--network", name, "--", "/bin/sleep", "300")
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _, code := command(t, "exec", consumer, "--", "/bin/wget", "-T", "1", "-qO-", "http://web:8080/")
		if code == 0 && strings.Contains(out, "named-network-ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery HTTP unavailable: %s", out)
		}
		time.Sleep(30 * time.Millisecond)
	}
	checkHost := func() {
		t.Helper()
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(10 * time.Second)
		for {
			response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
			if err == nil {
				body, e := io.ReadAll(response.Body)
				response.Body.Close()
				if e == nil && strings.Contains(string(body), "named-network-ok") {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("named network host publication unavailable")
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	checkHost()
	if out := success(t, "inspect", server); !strings.Contains(out, `"network": "`+name+`"`) || !strings.Contains(out, `"web"`) {
		t.Fatal(out)
	}
	success(t, "stop", server)
	if _, _, code := command(t, "network", "rm", name); code != 125 {
		t.Fatal("removed retained network")
	}
	success(t, "start", server)
	checkHost()
	if out := success(t, "exec", consumer, "--", "/bin/nslookup", "web.casklet"); !strings.Contains(out, "10.232.") {
		t.Fatal(out)
	}
	for _, id := range []string{server, consumer} {
		success(t, "stop", id)
		success(t, "rm", id)
	}
	success(t, "network", "rm", name)
}
