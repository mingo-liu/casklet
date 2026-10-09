//go:build linux

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
	"golang.org/x/sys/unix"
)

func importImage(t *testing.T, source string) string {
	t.Helper()
	id := strings.TrimSpace(backgroundSuccess(t, "image", "import", source))
	if err := image.ValidateID(id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		code, out, stderr, err := backgroundCommand(ctx, "image", "rm", id)
		if err != nil || (code != 0 && !strings.Contains(stderr, "image not found")) {
			t.Errorf("remove test image %s: exit=%d stdout=%q stderr=%q error=%v", id, code, out, stderr, err)
		}
	})
	return id
}

func imageTemplate(t *testing.T) string {
	t.Helper()
	source := executionTemplate(t)
	// Each test owns a unique content identity and can delete it independently.
	writeTemplateFile(t, source, "image-marker", fmt.Sprintf("marker-%d-%d", os.Getpid(), sequence.Add(1)), 0644)
	return source
}

func runImage(t *testing.T, id string, options []string, command ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"run", "--image", id}, options...)
	args = append(args, "--")
	args = append(args, command...)
	return start(t, "", args...).wait(t)
}

func imageSuccess(t *testing.T, id string, options []string, command ...string) string {
	t.Helper()
	code, out, stderr := runImage(t, id, options, command...)
	if code != 0 {
		t.Fatalf("image run exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	return out
}

func startImageContainer(t *testing.T, name, id string, command ...string) string {
	t.Helper()
	args := []string{"run", "--detach", "--name", name, "--image", id, "--"}
	args = append(args, command...)
	return backgroundID(t, backgroundSuccess(t, args...))
}

func assertImageInUse(t *testing.T, id string) {
	t.Helper()
	code, _, stderr := backgroundCLI(t, "image", "rm", id)
	if code != 125 || !strings.Contains(stderr, "referenced") {
		t.Fatalf("referenced image deletion: exit=%d stderr=%q", code, stderr)
	}
}

func TestImageImportIdentityAndForeground(t *testing.T) {
	require(t)
	source := imageTemplate(t)
	original, err := os.ReadFile(filepath.Join(source, "image-marker"))
	if err != nil {
		t.Fatal(err)
	}
	id := importImage(t, source)
	if duplicate := strings.TrimSpace(backgroundSuccess(t, "image", "import", source)); duplicate != id {
		t.Fatalf("duplicate import ID=%s want=%s", duplicate, id)
	}
	var records []image.Record
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "image", "ls", "--json")), &records); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range records {
		if record.ID == id {
			found = true
			if record.Architecture != runtime.GOARCH || record.SizeBytes <= 0 || record.CreatedAt.IsZero() {
				t.Fatalf("invalid metadata: %+v", record)
			}
		}
	}
	table := backgroundSuccess(t, "image", "ls")
	listed := false
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 5 && fields[0] == "<none>" && fields[1] == strings.TrimPrefix(id, "sha256:")[:12] {
			listed = true
		}
	}
	if !found || !listed || strings.Contains(table, id) {
		t.Fatal("imported image absent from list")
	}
	if out := imageSuccess(t, id, nil, "/bin/cat", "/image-marker"); out != string(original) {
		t.Fatalf("image content=%q", out)
	}
	imageSuccess(t, id, nil, "/bin/sh", "-c", "echo changed > /image-marker")
	if out := imageSuccess(t, id, nil, "/bin/cat", "/image-marker"); out != string(original) {
		t.Fatal("container mutated shared image")
	}
	writeTemplateFile(t, source, "image-marker", "new-source", 0644)
	if out := imageSuccess(t, id, nil, "/bin/cat", "/image-marker"); out != string(original) {
		t.Fatal("source mutation changed imported image")
	}
	changed := importImage(t, source)
	if changed == id {
		t.Fatal("changed content retained image ID")
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if out := imageSuccess(t, id, nil, "/bin/cat", "/image-marker"); out != string(original) {
		t.Fatal("image depends on source path")
	}
	data := t.TempDir()
	if err := os.Chmod(data, 0777); err != nil {
		t.Fatal(err)
	}
	imageSuccess(t, id, []string{"--user", "1000", "--read-only", "--mount", bindOption(data, "/data", false), "--workdir", "/data"}, "/bin/sh", "-c", "printf image-data > persistent")
	assertSourceFile(t, data, "persistent", "image-data")
	code, _, _ := runImage(t, id, nil, "/missing-command")
	if code != 125 {
		t.Fatalf("failed image startup exit=%d", code)
	}
	imageSuccess(t, id, nil, "/bin/true")
	backgroundSuccess(t, "image", "rm", id)
	code, _, stderr := runImage(t, id, nil, "/bin/true")
	if code != 125 || !strings.Contains(stderr, "image not found") {
		t.Fatalf("deleted image run exit=%d stderr=%q", code, stderr)
	}
}

func TestImageForegroundLease(t *testing.T) {
	require(t)
	id := importImage(t, imageTemplate(t))
	call := start(t, "", "run", "--image", id, "--", "/bin/sh", "-c", "trap 'exit 0' TERM; echo ready; while :; do sleep 1; done")
	pid := call.supervisor(t)
	assertImageInUse(t, id)
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := call.wait(t)
	if code != 0 {
		t.Fatalf("foreground stop exit=%d stderr=%q", code, stderr)
	}
	backgroundSuccess(t, "image", "rm", id)
}

func TestImageDetachedReferencesAndConcurrentUse(t *testing.T) {
	require(t)
	id := importImage(t, imageTemplate(t))
	first, second := backgroundName(t), backgroundName(t)
	firstID := startImageContainer(t, first, id, "/bin/sleep", "300")
	secondID := startImageContainer(t, second, id, "/bin/sleep", "300")
	backgroundSuccess(t, "exec", first, "--", "/bin/sh", "-c", "echo first > /private-data")
	backgroundSuccess(t, "exec", second, "--", "/bin/sh", "-c", "[ ! -e /private-data ]")
	assertImageInUse(t, id)
	var inspection container.Inspection
	if err := json.Unmarshal([]byte(backgroundSuccess(t, "inspect", firstID)), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Config.Image != id || inspection.Config.RootFS != "" {
		t.Fatalf("image identity or private paths leaked: %+v", inspection.Config)
	}
	for _, name := range []string{first, second} {
		backgroundSuccess(t, "stop", name)
	}
	assertImageInUse(t, id)
	backgroundSuccess(t, "rm", firstID)
	assertImageInUse(t, id)
	backgroundSuccess(t, "rm", secondID)
	failed := backgroundName(t)
	code, _, stderr := backgroundCLI(t, "run", "--detach", "--name", failed, "--image", id, "--", "/missing-command")
	if code != 125 {
		t.Fatalf("failed startup exit=%d stderr=%q", code, stderr)
	}
	waitBackground(t, failed, "failed")
	assertImageInUse(t, id)
	backgroundSuccess(t, "rm", failed)
	backgroundSuccess(t, "image", "rm", id)
}

func TestImageImportSafetyAndRollback(t *testing.T) {
	require(t)
	source := imageTemplate(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/proc", "/var/lib/casklet", link, link + "/bin", source + "/missing"} {
		code, out, stderr := backgroundCLI(t, "image", "import", path)
		if code != 125 || out != "" {
			t.Fatalf("unsafe import %s exit=%d stdout=%q stderr=%q", path, code, out, stderr)
		}
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("host-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "external-link")); err != nil {
		t.Fatal(err)
	}
	id := importImage(t, source)
	imageSuccess(t, id, nil, "/bin/sh", "-c", "[ -L /external-link ] && [ ! -e /external-link ]")
	if err := unix.Mkfifo(filepath.Join(source, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := backgroundCLI(t, "image", "import", source)
	if code != 125 || out != "" {
		t.Fatalf("special-file import exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	if err := os.Remove(filepath.Join(source, "fifo")); err != nil {
		t.Fatal(err)
	}
	mounted := filepath.Join(source, "mounted")
	if err := os.Mkdir(mounted, 0755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("tmpfs", mounted, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=1m"); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = backgroundCLI(t, "image", "import", source)
	if err := unix.Unmount(mounted, 0); err != nil {
		t.Fatal(err)
	}
	if code != 125 || out != "" || !strings.Contains(stderr, "mounted subtree") {
		t.Fatalf("mounted-source import exit=%d stdout=%q stderr=%q", code, out, stderr)
	}
	// Direct paths must not bypass image reference protection.
	code, _, stderr = run(t, []string{"--rootfs", filepath.Join("/var/lib/casklet/images", strings.TrimPrefix(id, "sha256:"), "rootfs")}, "/bin/true")
	if code != 125 || !strings.Contains(stderr, "require --image") {
		t.Fatalf("raw image path bypass exit=%d stderr=%q", code, stderr)
	}
	entries, err := os.ReadDir("/var/lib/casklet/images")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".import-") || strings.HasPrefix(entry.Name(), ".remove-") {
			t.Errorf("image operation leaked %s", entry.Name())
		}
	}
}

func TestImageConcurrentCreationAndRemoval(t *testing.T) {
	require(t)
	source := imageTemplate(t)
	id := importImage(t, source)
	for i := 0; i < 3; i++ {
		if got := strings.TrimSpace(backgroundSuccess(t, "image", "import", source)); got != id {
			t.Fatal("reimport changed identity")
		}
		name := backgroundName(t)
		type result struct {
			code        int
			out, stderr string
			err         error
		}
		launched, removed := make(chan result, 1), make(chan result, 1)
		ready := make(chan struct{})
		go func() {
			<-ready
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			code, out, stderr, err := backgroundCommand(ctx, "run", "--detach", "--name", name, "--image", id, "--", "/bin/sleep", "300")
			launched <- result{code, out, stderr, err}
		}()
		go func() {
			<-ready
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			code, out, stderr, err := backgroundCommand(ctx, "image", "rm", id)
			removed <- result{code, out, stderr, err}
		}()
		close(ready)
		launch, remove := <-launched, <-removed
		if launch.err != nil || remove.err != nil {
			t.Fatalf("concurrent operations: launch=%+v remove=%+v", launch, remove)
		}
		if launch.code == 0 {
			if remove.code != 125 || !strings.Contains(remove.stderr, "referenced") {
				t.Fatalf("live image removed: launch=%+v remove=%+v", launch, remove)
			}
			backgroundSuccess(t, "stop", name)
			backgroundSuccess(t, "rm", name)
			backgroundSuccess(t, "image", "rm", id)
		} else if launch.code != 125 || !strings.Contains(launch.stderr, "image not found") || remove.code != 0 {
			t.Fatalf("inconsistent creation/removal: launch=%+v remove=%+v", launch, remove)
		}
	}
}

func TestImageRemovalRefusesMountedStorage(t *testing.T) {
	require(t)
	source := imageTemplate(t)
	if err := os.Mkdir(filepath.Join(source, "data"), 0755); err != nil {
		t.Fatal(err)
	}
	id := importImage(t, source)
	target := filepath.Join("/var/lib/casklet/images", strings.TrimPrefix(id, "sha256:"), "rootfs/data")
	if err := unix.Mount("tmpfs", target, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=1m"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Unmount(target, 0); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile(filepath.Join(target, "keep"), []byte("mounted-data"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := backgroundCLI(t, "image", "rm", id)
	if code != 125 || !strings.Contains(stderr, "mounted subtree") {
		t.Fatalf("mounted image removal exit=%d stderr=%q", code, stderr)
	}
	assertSourceFile(t, target, "keep", "mounted-data")
}

func TestImageSupervisorRecoveryRetainsReference(t *testing.T) {
	require(t)
	id := importImage(t, imageTemplate(t))
	name := backgroundName(t)
	containerID := startImageContainer(t, name, id, "/bin/sleep", "300")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	output, err := exec.CommandContext(ctx, "systemctl", "kill", "--kill-whom=main", "--signal=SIGKILL", "casklet-"+containerID+".service").CombinedOutput()
	cancel()
	if err != nil {
		t.Fatalf("kill image supervisor: %v: %s", err, output)
	}
	waitBackground(t, containerID, "failed")
	assertBackgroundUnitStopped(t, containerID)
	assertImageInUse(t, id)
	backgroundSuccess(t, "rm", containerID)
	imageSuccess(t, id, nil, "/bin/true")
	backgroundSuccess(t, "image", "rm", id)
}
