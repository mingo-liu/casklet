//go:build darwin

package macos

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMachineRepairsIncompleteProvisioning(t *testing.T) {
	client(t)
	// This fault injection temporarily hides a guest package tool. Keep it off daily VMs.
	if os.Getenv("CASKLET_MACOS_RESIZE_GROW") != "1" {
		t.Skip("provisioning fault injection requires the disposable macOS runner")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	limaHome, err := filepath.EvalSymlinks(os.Getenv("LIMA_HOME"))
	if err != nil || !filepath.IsAbs(limaHome) {
		t.Fatal("provisioning fault injection requires an absolute disposable LIMA_HOME")
	}
	daily := filepath.Join(home, ".lima")
	if resolved, err := filepath.EvalSymlinks(daily); err == nil {
		daily = resolved
	}
	if limaHome == daily {
		t.Fatal("provisioning fault injection refuses the default Lima home")
	}
	success(t, "doctor")
	volume := fmt.Sprintf("provision-test-%d", time.Now().UnixNano())
	success(t, "volume", "create", volume)
	t.Cleanup(func() { command(t, "volume", "rm", volume) })
	id := detached(t, "--mount", "type=volume,source="+volume+",target=/data", "--", "/bin/sleep", "600")
	success(t, "exec", id, "--", "/bin/sh", "-c", "echo provision-preserved > /data/message; echo private-preserved > /private-message")
	fingerprint := func() string {
		t.Helper()
		output, err := guestRootCommand(t, "/bin/sh", "-c", `set -eu
sha256sum /usr/local/bin/casklet /usr/local/lib/casklet/engine.sha256
cat /proc/sys/kernel/random/boot_id
systemctl show --property=InvocationID --value "user@$SUDO_UID.service"`)
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	before := fingerprint()
	backup, err := guestRootCommand(t, "mktemp", "-d", "/var/tmp/casklet-provision-test-XXXXXXXX")
	if err != nil {
		t.Fatal(err)
	}
	backup = strings.TrimSpace(backup)
	if !strings.HasPrefix(backup, "/var/tmp/casklet-provision-test-") {
		t.Fatalf("unexpected backup: %q", backup)
	}
	if _, err := guestRootCommand(t, "/bin/sh", "-c", `set -eu
test ! -e /usr/sbin/apt-get
test ! -L /usr/sbin/apt-get
cp -a /usr/local/lib/casklet/provisioned-v1 "$1/marker"`, "backup", backup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := guestRootCommand(t, "/bin/sh", "-c", `set -eu
rm -f /usr/sbin/apt-get
if [ -e "$1/readelf" ] || [ -L "$1/readelf" ]; then mv -T "$1/readelf" /usr/bin/readelf; fi
cp -a "$1/marker" /usr/local/lib/casklet/provisioned-v1
rm -rf -- "$1"`, "restore-provision", backup)
		if err != nil {
			t.Error(err)
		}
	})
	if _, err := guestRootCommand(t, "/bin/sh", "-c", `set -eu
printf '#!/bin/sh\necho simulated-package-outage >&2\nexit 42\n' > /usr/sbin/apt-get
chmod 0755 /usr/sbin/apt-get
rm /usr/local/lib/casklet/provisioned-v1`, "fail-provision"); err != nil {
		t.Fatal(err)
	}
	_, diagnostic, code := command(t, "doctor")
	if code != 125 || !strings.Contains(diagnostic, "runtime machine initialization failed") || !strings.Contains(diagnostic, "retry casklet machine start") {
		t.Fatalf("missing recovery diagnostic: %d %q", code, diagnostic)
	}
	if _, err := guestRootCommand(t, "test", "!", "-e", "/usr/local/lib/casklet/provisioned-v1"); err != nil {
		t.Fatal("failed initialization published readiness", err)
	}
	if _, err := guestRootCommand(t, "rm", "/usr/sbin/apt-get"); err != nil {
		t.Fatal(err)
	}
	repair := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, client(t), args...)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			t.Fatalf("repair %q failed: %v %s", args, err, &output)
		}
		if !strings.Contains(output.String(), "Preparing runtime machine dependencies") {
			t.Fatalf("repair was not attempted: %s", &output)
		}
	}
	repair("machine", "start")
	// A matching old marker must not hide missing package dependencies either.
	if _, err := guestRootCommand(t, "mv", "/usr/bin/readelf", backup+"/readelf"); err != nil {
		t.Fatal(err)
	}
	repair("doctor")
	if after := fingerprint(); after != before {
		t.Fatalf("repair changed the engine, boot or user-manager execution: before=%q after=%q", before, after)
	}
	if output := success(t, "exec", id, "--", "/bin/sh", "-c", "cat /data/message /private-message"); output != "provision-preserved\nprivate-preserved\n" {
		t.Fatalf("repair lost workload data: %q", output)
	}
	_, diagnostic, code = command(t, "doctor")
	if code != 0 || diagnostic != "" {
		t.Fatalf("healthy reuse repeated initialization: %d %q", code, diagnostic)
	}
}
