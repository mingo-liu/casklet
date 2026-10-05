//go:build linux

package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func testStatePaths(t *testing.T) (string, statePaths) {
	t.Helper()
	base := t.TempDir()
	paths := statePaths{
		runsRoot: filepath.Join(base, "runs"), cgroupRoot: filepath.Join(base, "cgroups"),
		mountInfo: filepath.Join(base, "mountinfo"),
		// Fixtures are owned by the unprivileged test user. Production also
		// enforces root ownership; structural checks remain active in fixtures.
		checkDirectory: func(path string) error {
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
				return errors.New("unsafe fixture directory")
			}
			return nil
		},
	}
	for _, path := range []string{paths.runsRoot, paths.cgroupRoot} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(paths.mountInfo, nil, 0600); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(paths.runsRoot, "run-fixture")
	if err := os.Mkdir(run, 0700); err != nil {
		t.Fatal(err)
	}
	return run, paths
}

func TestRunLockExcludesRecoveryWhileInitHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	lock, err := openRunLock(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	// Duplicate the same open file description, as exec inheritance does.
	fd, err := unix.Dup(int(lock.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	inherited := os.NewFile(uintptr(fd), "init-lock")
	defer inherited.Close()
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if recovery, err := openRunLock(path, false); !errors.Is(err, unix.EWOULDBLOCK) {
		if recovery != nil {
			recovery.Close()
		}
		t.Fatalf("recovery acquired a live init lock: %v", err)
	}
	if err := inherited.Close(); err != nil {
		t.Fatal(err)
	}
	recovery, err := openRunLock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	recovery.Close()
}

func TestRecordReplacesCompleteMetadata(t *testing.T) {
	run, _ := testStatePaths(t)
	dir := &runDirectory{path: run}
	for _, group := range []string{"/first/container-a", "/second/container-b"} {
		if err := dir.record(group); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(run, "state.json"))
		if err != nil || string(data) != `{"cgroup":"`+group+`"}` {
			t.Fatalf("incomplete state record: %q, %v", data, err)
		}
	}
	entries, err := os.ReadDir(run)
	if err != nil || len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("temporary state record leaked: %v, %v", entries, err)
	}
}

func TestReclaimRunWithExpiredScope(t *testing.T) {
	run, paths := testStatePaths(t)
	dir := &runDirectory{path: run}
	if err := dir.record(filepath.Join(paths.cgroupRoot, "expired.scope", "container-gone")); err != nil {
		t.Fatal(err)
	}
	if err := reclaimRunAt(run, paths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(run); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale run not reclaimed: %v", err)
	}
}

func TestReclaimRunPreservesLiveCgroup(t *testing.T) {
	run, paths := testStatePaths(t)
	group := filepath.Join(paths.cgroupRoot, "live.scope", "container-live")
	if err := os.MkdirAll(group, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(group, "cgroup.events"), []byte("populated 1\nfrozen 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&runDirectory{path: run}).record(group); err != nil {
		t.Fatal(err)
	}
	if err := reclaimRunAt(run, paths); err == nil {
		t.Fatal("live cgroup reclaimed")
	}
	if _, err := os.Stat(run); err != nil {
		t.Fatalf("live run removed: %v", err)
	}
}

func TestReclaimRunPreservesMountReferences(t *testing.T) {
	for _, mount := range []string{"root", "target", "escaped"} {
		t.Run(mount, func(t *testing.T) {
			run, paths := testStatePaths(t)
			root, target := "/", "/unrelated"
			switch mount {
			case "root":
				root = run + "/rootfs"
			case "target":
				target = run + "/rootfs/proc"
			case "escaped":
				old := run
				run = strings.Replace(run, "run-fixture", "run-with space", 1)
				if err := os.Rename(old, run); err != nil {
					t.Fatal(err)
				}
				target = strings.ReplaceAll(run, " ", `\040`)
			}
			line := "23 22 0:1 " + root + " " + target + " rw - tmpfs tmpfs rw\n"
			if err := os.WriteFile(paths.mountInfo, []byte(line), 0600); err != nil {
				t.Fatal(err)
			}
			if err := reclaimRunAt(run, paths); err == nil {
				t.Fatal("mounted run reclaimed")
			}
			if _, err := os.Stat(run); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoveryRejectsUnsafeDeletionTargets(t *testing.T) {
	run, paths := testStatePaths(t)
	for _, candidate := range []string{paths.runsRoot, filepath.Dir(paths.runsRoot), run + "/..", filepath.Join(run, "run-nested")} {
		if err := reclaimRunAt(candidate, paths); err == nil {
			t.Fatalf("unsafe cleanup path accepted: %s", candidate)
		}
	}
	link := filepath.Join(paths.runsRoot, "run-link")
	if err := os.Symlink(run, link); err != nil {
		t.Fatal(err)
	}
	if err := reclaimRunAt(link, paths); err == nil {
		t.Fatal("symlink cleanup target accepted")
	}
	if _, err := os.Stat(run); err != nil {
		t.Fatal("original directory removed")
	}
}

func TestRecoveryRejectsUnownedCgroupTargets(t *testing.T) {
	for _, name := range []string{"container-top", "scope/not-container", "scope/container-", "scope/../scope/container-other"} {
		t.Run(name, func(t *testing.T) {
			run, paths := testStatePaths(t)
			if err := (&runDirectory{path: run}).record(paths.cgroupRoot + "/" + name); err != nil {
				t.Fatal(err)
			}
			if err := reclaimRunAt(run, paths); err == nil {
				t.Fatal("unowned cgroup target accepted")
			}
			if _, err := os.Stat(run); err != nil {
				t.Fatal("invalid metadata run removed")
			}
		})
	}
}

func TestCgroupEmptyRequiresExactValidCounter(t *testing.T) {
	for _, tc := range []struct {
		input string
		empty bool
		valid bool
	}{
		{"populated 0\nfrozen 0\n", true, true},
		{"populated 1\n", false, true},
		{"unpopulated 0\n", false, false},
		{"populated 0\npopulated 1\n", false, false},
		{"populated 2\n", false, false},
		{"populated 18446744073709551616\n", false, false},
		{"populated -1\n", false, false},
	} {
		empty, err := cgroupEmpty(tc.input)
		if (err == nil) != tc.valid || empty != tc.empty {
			t.Fatalf("%q: got empty=%v, err=%v", tc.input, empty, err)
		}
	}
}
