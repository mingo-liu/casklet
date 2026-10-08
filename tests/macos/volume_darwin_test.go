//go:build darwin

package macos

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNamedVolumeIsGuestLocalAndSurvivesContainerRemoval(t *testing.T) {
	name := fmt.Sprintf("mac-volume-%d", time.Now().UnixNano())
	success(t, "volume", "create", name)
	t.Cleanup(func() { command(t, "volume", "rm", name) })
	mount := "type=volume,source=" + name + ",target=/data"
	id := detached(t, "--mount", mount, "--", "/bin/sh", "-c", "echo persisted > /data/message; sleep 300")
	deadline := time.Now().Add(5 * time.Second)
	for {
		out, _, code := command(t, "exec", id, "--", "/bin/cat", "/data/message")
		if code == 0 && out == "persisted\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("volume write missing")
		}
		time.Sleep(30 * time.Millisecond)
	}
	success(t, "stop", id)
	if _, _, code := command(t, "volume", "rm", name); code != 125 {
		t.Fatal("removed referenced volume")
	}
	if out := success(t, "inspect", id); !strings.Contains(out, `"source": "`+name+`"`) {
		t.Fatal(out)
	}
	success(t, "rm", id)
	if out := success(t, "run", "--mount", mount+",readonly", "--", "/bin/cat", "/data/message"); out != "persisted\n" {
		t.Fatal(out)
	}
	success(t, "volume", "rm", name)
}
