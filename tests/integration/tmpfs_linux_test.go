//go:build linux

package integration

import (
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/container"
)

// All sizes are page-aligned, so df reports the configured capacity exactly.
const tmpfsCapacityProbe = `test "$(df -Pk /tmp | tail -n 1 | awk '{print $2}')" = 32768 && test "$(df -Pk /dev/shm | tail -n 1 | awk '{print $2}')" = 98304`

func TestTmpfsCapacitiesAcrossCopyOverlayHealthExecAndRestart(t *testing.T) {
	require(t)
	image := importImage(t, imageTemplate(t))
	for _, source := range []string{"copy", "overlay"} {
		t.Run(source, func(t *testing.T) {
			name := backgroundName(t)
			options := []string{"--tmp-size", "32m", "--shm-size", "96m", "--memory", "256m", "--user", "1000:1000", "--read-only", "--health-cmd", tmpfsCapacityProbe, "--health-interval", "100ms", "--health-timeout", "1s"}
			if source == "overlay" {
				args := append([]string{"run", "-d", "--name", name, "--image", image}, options...)
				args = append(args, "--", "/bin/sleep", "300")
				backgroundSuccess(t, args...)
			} else {
				startBackground(t, name, options, "/bin/sleep", "300")
			}
			check := func() {
				t.Helper()
				i := awaitHealth(t, name, container.HealthHealthy)
				if i.Config.TmpSize != 32<<20 || i.Config.ShmSize != 96<<20 {
					t.Fatalf("inspection: %+v", i.Config)
				}
				backgroundSuccess(t, "exec", name, "--", "/bin/sh", "-c", tmpfsCapacityProbe+` && test "$(stat -c %a /tmp):$(stat -c %a /dev/shm)" = 1777:1777 && test ! -e /tmp/allocated && test ! -e /dev/shm/allocated`)
				mounts := backgroundSuccess(t, "exec", name, "--", "/bin/cat", "/proc/mounts")
				for _, target := range []string{"/tmp", "/dev/shm"} {
					found := false
					for _, line := range strings.Split(mounts, "\n") {
						fields := strings.Fields(line)
						if len(fields) >= 4 && fields[1] == target {
							found = true
							for _, flag := range []string{"rw", "nosuid", "nodev", "noexec"} {
								if !strings.Contains(","+fields[3]+",", ","+flag+",") {
									t.Fatalf("missing %s: %s", flag, line)
								}
							}
						}
					}
					if !found {
						t.Fatalf("missing %s tmpfs", target)
					}
				}
			}
			check()
			before := statsBackground(t, name)
			backgroundSuccess(t, "exec", name, "--", "/bin/sh", "-c", "dd if=/dev/zero of=/tmp/allocated bs=1M count=20 2>/dev/null && dd if=/dev/zero of=/dev/shm/allocated bs=1M count=68 2>/dev/null")
			after := statsBackground(t, name)
			if before.MemoryBytes == nil || after.MemoryBytes == nil || *after.MemoryBytes < *before.MemoryBytes+(80<<20) {
				t.Fatalf("tmpfs pages not charged: before=%+v after=%+v", before, after)
			}
			backgroundSuccess(t, "stop", name)
			backgroundSuccess(t, "start", name)
			check()
			backgroundSuccess(t, "restart", name)
			check()
		})
	}
}

func TestTmpfsCapacityExhaustionAndMemoryCeiling(t *testing.T) {
	require(t)
	if out := success(t, []string{"--tmp-size", "1", "--shm-size", "1"}, "/bin/sh", "-c", `test "$(stat -f -c %b /tmp):$(stat -f -c %b /dev/shm)" = 1:1 && echo rounded-ok`); out != "rounded-ok\n" {
		t.Fatal(out)
	}
	probe := `for target in /tmp /dev/shm; do message=$(dd if=/dev/zero of=$target/full bs=1M count=4 2>&1); code=$?; test "$code" -ne 0 || exit 91; echo "$message" | grep -q 'No space left on device' || exit 92; done; test "$(stat -c %s /tmp/full)" = 2097152 && test "$(stat -c %s /dev/shm/full)" = 3145728; echo capacity-ok`
	if out := success(t, []string{"--tmp-size", "2m", "--shm-size", "3m", "--user", "1000:1000", "--read-only"}, "/bin/sh", "-c", probe); out != "capacity-ok\n" {
		t.Fatal(out)
	}
	// Capacity can exceed --memory; actual writes remain bounded by the cgroup.
	for _, target := range []string{"/tmp", "/dev/shm"} {
		code, out, diagnostic := run(t, []string{"--tmp-size", "96m", "--shm-size", "96m", "--memory", "32m", "--timeout", "15s"}, "/bin/dd", "if=/dev/zero", "of="+target+"/allocated", "bs=1M", "count=64")
		if code != 137 || !strings.Contains(diagnostic, "OOM") {
			t.Fatalf("%s memory ceiling: %d %s %s", target, code, out, diagnostic)
		}
	}
}
