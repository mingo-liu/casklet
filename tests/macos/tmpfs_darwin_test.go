//go:build darwin

package macos

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
)

const macTmpfsCapacityProbe = `test "$(df -Pk /tmp | tail -n 1 | awk '{print $2}')" = 32768 && test "$(df -Pk /dev/shm | tail -n 1 | awk '{print $2}')" = 98304`

func TestTmpfsRunConfigProjectHealthExecAndRestartThroughMac(t *testing.T) {
	client(t)
	file := filepath.Join(t.TempDir(), "run.json")
	run := map[string]any{"tmp-size": "16m", "shm-size": "96m", "memory": "256m", "read-only": true, "user": "1000:1000", "health-cmd": macTmpfsCapacityProbe, "health-interval": "100ms", "health-timeout": "1s", "command": []string{"/bin/sleep", "300"}}
	write := func(doc any) {
		t.Helper()
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(run)
	id := detached(t, "--config", file, "--tmp-size", "32m")
	check := func(ref string) {
		t.Helper()
		i := macHealth(t, ref, container.HealthHealthy)
		if i.Config.TmpSize != 32<<20 || i.Config.ShmSize != 96<<20 {
			t.Fatalf("inspection: %+v", i.Config)
		}
		success(t, "exec", ref, "--", "/bin/sh", "-c", macTmpfsCapacityProbe+` && test "$(stat -c %a /tmp):$(stat -c %a /dev/shm)" = 1777:1777 && test ! -e /tmp/allocated && test ! -e /dev/shm/allocated`)
	}
	check(id)
	success(t, "exec", id, "--", "/bin/sh", "-c", "dd if=/dev/zero of=/tmp/allocated bs=1M count=20 2>/dev/null && dd if=/dev/zero of=/dev/shm/allocated bs=1M count=68 2>/dev/null")
	// Host configuration files are unnecessary after creation.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	success(t, "stop", id)
	success(t, "start", id)
	check(id)
	success(t, "restart", id)
	check(id)

	name := fmt.Sprintf("tmpfs-%x", time.Now().UnixNano())
	run["tmp-size"] = "32m"
	write(map[string]any{"name": name, "services": map[string]any{"app": map[string]any{"run": run}}})
	t.Cleanup(func() { command(t, "project", "down", "--volumes", name) })
	success(t, "project", "up", "--config", file, "--wait", "--timeout", "1m")
	ref := "cp-" + name + "-app"
	check(ref)
	success(t, "project", "up", "--config", file, "--wait", "--timeout", "1m")
	success(t, "restart", ref)
	check(ref)
}

func TestTmpfsCapacityExhaustionThroughMac(t *testing.T) {
	probe := `for target in /tmp /dev/shm; do message=$(dd if=/dev/zero of=$target/full bs=1M count=4 2>&1); code=$?; test "$code" -ne 0 || exit 91; echo "$message" | grep -q 'No space left on device' || exit 92; done; test "$(stat -c %s /tmp/full)" = 2097152 && test "$(stat -c %s /dev/shm/full)" = 3145728; echo capacity-ok`
	if out := success(t, "run", "--tmp-size", "2m", "--shm-size", "3m", "--user", "1000:1000", "--read-only", "--", "/bin/sh", "-c", probe); out != "capacity-ok\n" {
		t.Fatal(out)
	}
	for _, flag := range []string{"--tmp-size", "--shm-size"} {
		if out, diagnostic, code := command(t, "run", flag, "0", "--", "/bin/true"); code != 125 || out != "" || !strings.Contains(diagnostic, "positive") {
			t.Fatalf("explicit zero %s: %d %s %s", flag, code, out, diagnostic)
		}
	}
}
