//go:build darwin

package macos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/machine"
)

// Disk growth cannot be undone. Run this case only against an explicitly
// disposable LIMA_HOME, never as part of a daily-use machine regression run.
func TestMachineResizePreservesDataAndGrowsFilesystem(t *testing.T) {
	client(t)
	if os.Getenv("CASKLET_MACOS_RESIZE_GROW") != "1" {
		t.Skip("set CASKLET_MACOS_RESIZE_GROW=1 with a disposable LIMA_HOME to test irreversible disk growth")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	limaHome := os.Getenv("LIMA_HOME")
	if !filepath.IsAbs(limaHome) {
		t.Fatal("resize growth test requires an absolute disposable LIMA_HOME")
	}
	realLimaHome, err := filepath.EvalSymlinks(limaHome)
	if err != nil {
		t.Fatal(err)
	}
	dailyLimaHome := filepath.Join(home, ".lima")
	if resolved, err := filepath.EvalSymlinks(dailyLimaHome); err == nil {
		dailyLimaHome = resolved
	}
	if realLimaHome == dailyLimaHome {
		t.Fatal("resize growth test refuses the default Lima home")
	}
	lima := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		data, err := exec.CommandContext(ctx, "limactl", append([]string{"--tty=false"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("Lima %q: %v %s", args, err, data)
		}
		return string(data)
	}
	resources := func() (machine.Instance, map[string]any) {
		t.Helper()
		data := lima("list", "--json", machine.Name)
		var instance machine.Instance
		var record struct {
			Config map[string]any `json:"config"`
		}
		if err := json.Unmarshal([]byte(data), &instance); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(data), &record); err != nil {
			t.Fatal(err)
		}
		delete(record.Config, "cpus")
		delete(record.Config, "memory")
		delete(record.Config, "disk")
		return instance, record.Config
	}
	success(t, "doctor")
	before, config := resources()
	if before.Disk%(1<<30) != 0 || before.Disk >= 1024*(1<<30) || before.Memory%(1<<30) != 0 {
		t.Fatal("test requires whole-GiB memory and a whole-GiB disk below the size limit")
	}
	if active := strings.TrimSpace(success(t, "ps", "--json")); active != "[]" {
		t.Fatalf("resize test requires no running containers: %s", active)
	}

	directory := hostDirectory(t)
	rootfs := filepath.Join(directory, "template")
	success(t, "rootfs", rootfs)
	if err := os.WriteFile(filepath.Join(rootfs, "seed"), []byte("image-preserved\n"), 0644); err != nil {
		t.Fatal(err)
	}
	image := strings.TrimSpace(success(t, "image", "import", rootfs))
	t.Cleanup(func() { command(t, "image", "rm", image) })
	volume := fmt.Sprintf("resize-volume-%d", time.Now().UnixNano())
	success(t, "volume", "create", volume)
	t.Cleanup(func() { command(t, "volume", "rm", volume) })
	id := detached(t, "--image", image, "--mount", "type=volume,source="+volume+",target=/data", "--", "/bin/sleep", "300")
	success(t, "exec", id, "--", "/bin/sh", "-c", "echo container-preserved > /retained; echo volume-preserved > /data/value; echo ready > /count")
	waitForContents(t, id, "ready\n")
	success(t, "stop", id)

	if out, diagnostic, code := command(t, "machine", "resize", "--cpus", "2"); code != 125 || out != "" || !strings.Contains(diagnostic, "stop the runtime machine") {
		t.Fatalf("running resize: %d %s %s", code, out, diagnostic)
	}
	fsSize := func() int64 {
		t.Helper()
		value := strings.TrimSpace(lima("shell", machine.Name, "/bin/sh", "-c", "df -B1 --output=size / | tail -1"))
		size, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			t.Fatalf("filesystem size: %s %v", value, err)
		}
		return size
	}
	oldSize := fsSize()
	success(t, "machine", "stop")
	t.Cleanup(func() { command(t, "machine", "start") })
	if out, diagnostic, code := command(t, "machine", "resize", "--disk", strconv.FormatInt(before.Disk/(1<<30)-1, 10)); code != 125 || out != "" || !strings.Contains(diagnostic, "shrinking") {
		t.Fatalf("shrinking resize: %d %s %s", code, out, diagnostic)
	}
	disk := strconv.FormatInt(before.Disk/(1<<30)+1, 10)
	cpus := 2
	if before.CPUs == cpus {
		cpus = 3
	}
	success(t, "machine", "resize", "--cpus", strconv.Itoa(cpus), "--memory", "2", "--disk", disk)
	after, afterConfig := resources()
	if after.Status != "Stopped" || after.CPUs != cpus || after.Memory != 2*(1<<30) || after.Disk != before.Disk+(1<<30) || !reflect.DeepEqual(config, afterConfig) {
		t.Fatalf("resize configuration changed unrelated settings: %+v\nbefore=%v\nafter=%v", after, config, afterConfig)
	}
	if out := success(t, "machine", "status"); !strings.Contains(out, "disk="+disk+"GiB (configured)") {
		t.Fatal(out)
	}
	if out := success(t, "machine", "resize", "--cpus", strconv.Itoa(cpus), "--memory", "2", "--disk", disk); !strings.Contains(out, "no changes") {
		t.Fatal(out)
	}
	success(t, "machine", "start")
	if newSize := fsSize(); newSize <= oldSize {
		t.Fatalf("root filesystem did not grow: %d <= %d", newSize, oldSize)
	}
	if actual := strings.TrimSpace(lima("shell", machine.Name, "nproc")); actual != strconv.Itoa(cpus) {
		t.Fatalf("guest CPUs: %s", actual)
	}
	mem := strings.TrimSpace(lima("shell", machine.Name, "/bin/sh", "-c", "awk '/MemTotal:/ {print $2}' /proc/meminfo"))
	memKiB, err := strconv.ParseInt(mem, 10, 64)
	if err != nil || memKiB < 1500*1024 || memKiB > 2*1024*1024 {
		t.Fatalf("guest memory: %s %v", mem, err)
	}
	success(t, "start", id)
	if out := success(t, "exec", id, "--", "/bin/cat", "/retained", "/seed", "/data/value"); out != "container-preserved\nimage-preserved\nvolume-preserved\n" {
		t.Fatalf("data after resize: %s", out)
	}
	success(t, "stop", id)
	// Restore CPU/memory for later tests in the same disposable suite.
	success(t, "machine", "stop")
	success(t, "machine", "resize", "--cpus", strconv.Itoa(before.CPUs), "--memory", strconv.FormatInt(before.Memory/(1<<30), 10))
	success(t, "machine", "start")
}
