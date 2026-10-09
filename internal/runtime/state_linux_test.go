//go:build linux

package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestRecoverRunWaitsForCompetingRecovery(t *testing.T) {
	for _, completion := range []string{"released lock", "removed directory"} {
		t.Run(completion, func(t *testing.T) {
			run, paths := testStatePaths(t)
			holder, err := openRunLock(filepath.Join(run, "lock"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			entered, proceed := make(chan struct{}), make(chan struct{})
			defer close(proceed)
			checks := 0
			check := paths.checkDirectory
			paths.checkDirectory = func(path string) error {
				if err := check(path); err != nil {
					return err
				}
				checks++
				if checks == 2 {
					// The first attempt already encountered the competing lock.
					close(entered)
					<-proceed
				}
				return nil
			}
			finished := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			go func() { finished <- recoverRunAt(ctx, run, paths) }()
			select {
			case <-entered:
			case err := <-finished:
				t.Fatalf("recovery did not retry a competing lock: %v", err)
			case <-ctx.Done():
				t.Fatal("recovery did not inspect the competing lock again")
			}
			if completion == "removed directory" {
				if err := os.RemoveAll(run); err != nil {
					t.Fatal(err)
				}
			}
			holder.Close()
			proceed <- struct{}{}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("competing recovery completion failed: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("recovery did not finish after the competing owner completed")
			}
			if _, err := os.Stat(run); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovered run remains: %v", err)
			}
		})
	}
}

func TestRecoverRunPreservesHeldLockOnCancellationAndDeadline(t *testing.T) {
	for _, cause := range []string{"canceled", "deadline"} {
		t.Run(cause, func(t *testing.T) {
			run, paths := testStatePaths(t)
			holder, err := openRunLock(filepath.Join(run, "lock"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Close()
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if cause == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			checked := paths.checkDirectory
			var once sync.Once
			paths.checkDirectory = func(path string) error {
				if cause == "canceled" {
					once.Do(cancel)
				}
				return checked(path)
			}
			if err := recoverRunAt(ctx, run, paths); !errors.Is(err, want) {
				t.Fatalf("caller cancellation lost: %v, want %v", err, want)
			}
			if _, err := os.Stat(run); err != nil {
				t.Fatalf("canceled recovery deleted a held run: %v", err)
			}
		})
	}
}

func TestRecoverRunLocalTimeoutPreservesBusyCauseAndStorage(t *testing.T) {
	run, paths := testStatePaths(t)
	holder, err := openRunLock(filepath.Join(run, "lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	started := time.Now()
	err = recoverRunAt(context.Background(), run, paths)
	if !errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("local timeout replaced the busy cause: %v", err)
	}
	if elapsed := time.Since(started); elapsed < runRecoveryLimit || elapsed > runRecoveryLimit+time.Second {
		t.Fatalf("local recovery bound: %s", elapsed)
	}
	if _, err := os.Stat(run); err != nil {
		t.Fatalf("busy run removed: %v", err)
	}
}

func TestRecoverRunWaitsForPopulatedCgroupAndPreservesTimeout(t *testing.T) {
	for _, completion := range []string{"drained", "still populated"} {
		t.Run(completion, func(t *testing.T) {
			run, paths := testStatePaths(t)
			lock, err := openRunLock(filepath.Join(run, "lock"), true)
			if err != nil {
				t.Fatal(err)
			}
			lock.Close()
			group := filepath.Join(paths.cgroupRoot, "stopping.scope", "container-draining")
			if err := os.MkdirAll(group, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(group, "cgroup.events"), []byte("populated 1\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := (&runDirectory{path: run}).record(group); err != nil {
				t.Fatal(err)
			}
			if completion == "still populated" {
				if err := recoverRunAt(context.Background(), run, paths); !errors.Is(err, errRunPopulated) || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("populated recovery timeout lost cause: %v", err)
				}
				if _, err := os.Stat(run); err != nil {
					t.Fatalf("populated run removed: %v", err)
				}
				return
			}
			entered, proceed := make(chan struct{}), make(chan struct{})
			defer close(proceed)
			check := paths.checkDirectory
			checks := 0
			paths.checkDirectory = func(path string) error {
				if err := check(path); err != nil {
					return err
				}
				checks++
				if checks == 3 {
					// One lock attempt and one reclaim attempt already observed
					// the populated cgroup; this is the next lock inspection.
					close(entered)
					<-proceed
				}
				return nil
			}
			finished := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			go func() { finished <- recoverRunAt(ctx, run, paths) }()
			select {
			case <-entered:
			case err := <-finished:
				t.Fatalf("recovery did not retry a populated cgroup: %v", err)
			case <-ctx.Done():
				t.Fatal("populated recovery did not retry")
			}
			// Waiting must release the run lock, allowing other owners to inspect
			// it. Simulate systemd removing the now-drained fixture cgroup.
			inspector, err := openRunLock(filepath.Join(run, "lock"), false)
			if err != nil {
				t.Fatalf("populated retry kept the lock: %v", err)
			}
			inspector.Close()
			if err := os.RemoveAll(group); err != nil {
				t.Fatal(err)
			}
			proceed <- struct{}{}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("drained recovery failed: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("drained recovery did not complete")
			}
			if _, err := os.Stat(run); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("drained run remains: %v", err)
			}
		})
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

func TestRecoveryPreservesInvalidStateFilesWithoutBlocking(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			run, paths := testStatePaths(t)
			state := filepath.Join(run, "state.json")
			switch kind {
			case "symlink":
				target := filepath.Join(filepath.Dir(paths.runsRoot), "external-state.json")
				if err := os.WriteFile(target, []byte(`{"cgroup":"`+filepath.Join(paths.cgroupRoot, "expired.scope", "container-gone")+`"}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, state); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(state, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				file, err := os.Create(state)
				if err != nil {
					t.Fatal(err)
				}
				// A sparse file reproduces unbounded metadata without allocating it.
				if err := file.Truncate(1 << 30); err != nil {
					file.Close()
					t.Fatal(err)
				}
				file.Close()
			}
			finished := make(chan error, 1)
			go func() { finished <- reclaimRunAt(run, paths) }()
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("invalid state file reclaimed")
				}
			case <-time.After(time.Second):
				t.Fatal("state recovery blocked")
			}
			if _, err := os.Stat(run); err != nil {
				t.Fatalf("invalid state directory removed: %v", err)
			}
		})
	}
}

func TestRunPublicationWaitsForRecoveryInspection(t *testing.T) {
	_, paths := testStatePaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	inspection, err := acquireStateLock(ctx, paths.runsRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close()
	type result struct {
		run *runDirectory
		err error
	}
	created := make(chan result, 1)
	go func() {
		run, err := createRunAt(ctx, paths.runsRoot)
		created <- result{run, err}
	}()
	select {
	case got := <-created:
		if got.run != nil {
			got.run.lock.Close()
		}
		t.Fatalf("creation bypassed recovery coordination: %v", got.err)
	case <-time.After(30 * time.Millisecond):
	}
	entries, err := os.ReadDir(paths.runsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 { // Existing fixture and persistent .lock only.
		t.Fatalf("an unlocked run was published: %v", entries)
	}
	inspection.Close()
	var got result
	select {
	case got = <-created:
	case <-ctx.Done():
		t.Fatal("creation did not resume after recovery inspection")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.run.lock.Close()
	if lock, err := lockRecoveryCandidate(ctx, got.run.path, paths); !errors.Is(err, unix.EWOULDBLOCK) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("published run was not already locked: %v", err)
	}
}

func TestConcurrentCreationAndRecoveryPreserveActiveRuns(t *testing.T) {
	_, paths := testStatePaths(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	recoverCtx, stopRecovery := context.WithCancel(ctx)
	defer stopRecovery()
	recovered := make(chan error, 1)
	go func() {
		for recoverCtx.Err() == nil {
			if err := recoverRunsAt(recoverCtx, io.Discard, paths); err != nil {
				recovered <- err
				return
			}
		}
		recovered <- nil
	}()
	const count = 32
	type result struct {
		run *runDirectory
		err error
	}
	created := make(chan result, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			run, err := createRunAt(ctx, paths.runsRoot)
			created <- result{run, err}
		})
	}
	workers.Wait()
	close(created)
	runs := make([]*runDirectory, 0, count)
	for result := range created {
		if result.err != nil {
			t.Errorf("concurrent publication failed: %v", result.err)
			continue
		}
		runs = append(runs, result.run)
		defer result.run.lock.Close()
	}
	stopRecovery()
	if err := <-recovered; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, run := range runs {
		if _, err := os.Stat(run.path); err != nil {
			t.Errorf("recovery removed an active run: %v", err)
		}
	}
}

func TestStateCoordinationLockRespectsCancellation(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	holder, err := acquireStateLock(ctx, root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	canceled, stop := context.WithCancel(ctx)
	stop()
	if lock, err := acquireStateLock(canceled, root, true); !errors.Is(err, context.Canceled) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("canceled lock acquisition: %v", err)
	}
	busy, done := context.WithTimeout(ctx, 20*time.Millisecond)
	defer done()
	if lock, err := acquireStateLock(busy, root, true); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("busy lock ignored deadline: %v", err)
	}
	holder.Close()
	resumed, err := acquireStateLock(ctx, root, false)
	if err != nil {
		t.Fatalf("failed acquisition leaked coordination lock: %v", err)
	}
	resumed.Close()
}

func TestRecoveryReleasesCoordinationBeforeReclaim(t *testing.T) {
	run, paths := testStatePaths(t)
	lock, err := openRunLock(filepath.Join(run, "lock"), true)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	checked := paths.checkDirectory
	entered, proceed := make(chan struct{}), make(chan struct{})
	checks := 0
	paths.checkDirectory = func(path string) error {
		checks++
		if checks == 2 {
			close(entered)
			select {
			case <-proceed:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return checked(path)
	}
	recovered := make(chan error, 1)
	go func() { recovered <- recoverRunsAt(ctx, io.Discard, paths) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("recovery did not reach reclaim")
	}
	created, err := createRunAt(ctx, paths.runsRoot)
	if err != nil {
		t.Fatalf("slow reclaim held publication lock: %v", err)
	}
	defer created.lock.Close()
	close(proceed)
	if err := <-recovered; err != nil {
		t.Fatal(err)
	}
}

func TestStateCoordinationRejectsUnsafeLockFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "hardlink", "writable"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			lock := filepath.Join(root, ".lock")
			switch kind {
			case "symlink", "hardlink":
				target := filepath.Join(t.TempDir(), "external-lock")
				if err := os.WriteFile(target, nil, 0600); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if kind == "hardlink" {
					link = os.Link
				}
				if err := link(target, lock); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(lock, 0600); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.WriteFile(lock, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(lock, 0666); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if file, err := acquireStateLock(ctx, root, false); err == nil {
				file.Close()
				t.Fatal("unsafe coordination lock accepted")
			}
		})
	}
}
