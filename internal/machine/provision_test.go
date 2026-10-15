package machine

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type provisionFixture struct {
	directory string
	script    string
}

func newProvisionFixture(t *testing.T) provisionFixture {
	t.Helper()
	directory := t.TempDir()
	for _, name := range []string{"bin", "state", "locks", "apparmor", "systemd"} {
		if err := os.Mkdir(filepath.Join(directory, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"rm", "mkdir", "cat", "touch", "cp"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(directory, "bin", name)); err != nil {
			t.Fatal(err)
		}
	}
	stub := `#!/bin/sh
name=${0##*/}
printf '%s %s\n' "$name" "$*" >> "$FIXTURE/calls"
case "$name" in
id) echo 501 ;;
dpkg-query) test -f "$FIXTURE/packages-ready" && printf installed ;;
dpkg) test ! -f "$FIXTURE/pending-packages" || exit 1 ;;
apt-get)
    while [ -f "$FIXTURE/block-apt" ]; do /bin/sleep 0.02; done
    if [ -f "$FIXTURE/fail-apt" ]; then echo 'simulated package outage' >&2; exit 42; fi
    if [ -f "$FIXTURE/interrupt-apt" ]; then kill -TERM "$PPID"; exit 1; fi
    case " $* " in *' --fix-broken '*) rm -f "$FIXTURE/pending-packages" ;; esac
    case " $* " in *' install '*) touch "$FIXTURE/packages-ready"; cp "$FIXTURE/bin/busybox" "$FIXTURE/bin/readelf" ;; esac
    ;;
systemctl)
    case "$1" in
    show) if [ -f "$FIXTURE/delegated" ]; then echo 'cpu memory pids'; fi ;;
    restart) test ! -f "$FIXTURE/fail-service" || exit 43; touch "$FIXTURE/delegated" ;;
    esac
    ;;
esac
exit 0
`
	for _, name := range []string{"busybox", "ip", "nft", "unshare", "conntrack", "apparmor_parser", "dpkg-query", "dpkg", "apt-get", "id", "flock", "loginctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(directory, "bin", name), []byte(stub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer(
		"export PATH=/usr/sbin:/usr/bin:/sbin:/bin", "export PATH="+quote(filepath.Join(directory, "bin")),
		"/usr/local/lib/casklet", filepath.Join(directory, "state"),
		"/run/lock", filepath.Join(directory, "locks"),
		"/etc/apparmor.d", filepath.Join(directory, "apparmor"),
		"/etc/systemd/system", filepath.Join(directory, "systemd"),
	).Replace(guestProvisionScript)
	return provisionFixture{directory, script}
}

func (f provisionFixture) run(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{"-c", f.script, "casklet-provision"}, args...)...)
	cmd.Env = []string{"FIXTURE=" + f.directory, "SUDO_USER=guest", "SUDO_UID=501"}
	return cmd.CombinedOutput()
}

func (f provisionFixture) flag(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.directory, name), nil, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionRetriesIncompleteAndDamagedDependencies(t *testing.T) {
	f := newProvisionFixture(t)
	f.flag(t, "fail-apt")
	if output, err := f.run(t); err == nil || !strings.Contains(string(output), "simulated package outage") {
		t.Fatalf("outage was not preserved: %s %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(f.directory, "state/provisioned-v1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed initialization published a marker: %v", err)
	}
	if err := os.Remove(filepath.Join(f.directory, "fail-apt")); err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(t); err != nil {
		t.Fatalf("retry failed: %s %v", output, err)
	}
	calls, _ := os.ReadFile(filepath.Join(f.directory, "calls"))
	if !strings.Contains(string(calls), "systemctl restart user@501.service") {
		t.Fatalf("initial user delegation was not applied: %s", calls)
	}
	if output, err := f.run(t, "--check"); err != nil {
		t.Fatalf("successful provisioning is not ready: %s %v", output, err)
	}
	if err := os.Remove(filepath.Join(f.directory, "bin/readelf")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, "--check"); err == nil {
		t.Fatal("matching marker hid a missing dependency")
	}
	if err := os.WriteFile(filepath.Join(f.directory, "calls"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(t); err != nil {
		t.Fatalf("dependency repair failed: %s %v", output, err)
	}
	calls, _ = os.ReadFile(filepath.Join(f.directory, "calls"))
	if !strings.Contains(string(calls), "apt-get") || strings.Contains(string(calls), "systemctl restart") {
		t.Fatalf("repair did not preserve the delegated user manager: %s", calls)
	}
	if err := os.WriteFile(filepath.Join(f.directory, "calls"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := f.run(t); err != nil {
		t.Fatalf("healthy reuse failed: %s %v", output, err)
	}
	calls, _ = os.ReadFile(filepath.Join(f.directory, "calls"))
	if strings.Contains(string(calls), "apt-get") || strings.Contains(string(calls), "systemctl") {
		t.Fatalf("healthy reuse changed dependencies or services: %s", calls)
	}
}

func TestProvisionFailureAndInterruptionDoNotPublishReadiness(t *testing.T) {
	for _, failure := range []string{"interrupt-apt", "fail-service"} {
		t.Run(failure, func(t *testing.T) {
			f := newProvisionFixture(t)
			f.flag(t, failure)
			if output, err := f.run(t, "guest", "501"); err == nil {
				t.Fatalf("failure was ignored: %s", output)
			}
			if _, err := f.run(t, "--check"); err == nil {
				t.Fatal("failed initialization became ready")
			}
			if err := os.Remove(filepath.Join(f.directory, failure)); err != nil {
				t.Fatal(err)
			}
			if output, err := f.run(t, "guest", "501"); err != nil {
				t.Fatalf("retry failed: %s %v", output, err)
			}
		})
	}
}

func TestProvisionResumesInterruptedPackageConfiguration(t *testing.T) {
	f := newProvisionFixture(t)
	f.flag(t, "pending-packages")
	if output, err := f.run(t); err != nil {
		t.Fatalf("pending package recovery failed: %s %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(f.directory, "pending-packages")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending packages were not recovered: %v", err)
	}
	if output, err := f.run(t, "--check"); err != nil {
		t.Fatalf("recovered packages are not ready: %s %v", output, err)
	}
}

func TestProvisionPreviewAndCancellationDoNotRepair(t *testing.T) {
	m := &Machine{stderr: io.Discard}
	instance := Instance{SSHConfigFile: filepath.Join(t.TempDir(), "missing")}
	if err := m.ensureProvisioned(context.Background(), instance, true); err == nil || !strings.Contains(err.Error(), "casklet machine start") {
		t.Fatalf("preview did not reject incomplete initialization: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.ensureProvisioned(ctx, instance, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
}

func TestConcurrentProvisioningSharesGuestLock(t *testing.T) {
	flock, err := exec.LookPath("flock")
	if err != nil {
		t.Skip("real guest flock is exercised on Linux")
	}
	f := newProvisionFixture(t)
	if err := os.Remove(filepath.Join(f.directory, "bin/flock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(flock, filepath.Join(f.directory, "bin/flock")); err != nil {
		t.Fatal(err)
	}
	f.flag(t, "block-apt")
	t.Cleanup(func() { _ = os.Remove(filepath.Join(f.directory, "block-apt")) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", f.script, "casklet-provision", "guest", "501")
		cmd.Env = []string{"FIXTURE=" + f.directory}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first := start()
	for {
		calls, _ := os.ReadFile(filepath.Join(f.directory, "calls"))
		if strings.Contains(string(calls), "apt-get") {
			break
		}
		if ctx.Err() != nil {
			_ = first.Wait()
			t.Fatal("first provisioner did not acquire the guest lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := start()
	if err := os.Remove(filepath.Join(f.directory, "block-apt")); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []*exec.Cmd{first, second} {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("concurrent provisioning failed: %v", err)
		}
	}
	calls, _ := os.ReadFile(filepath.Join(f.directory, "calls"))
	if strings.Count(string(calls), "apt-get ") != 2 || strings.Count(string(calls), "systemctl restart ") != 1 {
		t.Fatalf("provisioners repeated package installation or delegation: %s", calls)
	}
}
