//go:build linux

package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"golang.org/x/sys/unix"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := newStoreAt(filepath.Join(t.TempDir(), "containers"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testConfig() config.Config {
	return config.Config{RootFS: "/rootfs", Hostname: "test", Memory: 8 * 1024 * 1024, PidsLimit: 8, Command: []string{"/bin/echo", "hello"}}
}

func createTestRecord(t *testing.T, store *Store, name string) Record {
	t.Helper()
	record, err := store.Create(context.Background(), testConfig(), name)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestStorePersistsConfigurationAndReferences(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "web")
	if !containerID.MatchString(record.ID) || record.State != StateCreated {
		t.Fatalf("unexpected new record: %+v", record)
	}
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	if record.BootID != bootID || !bootIDPattern.MatchString(record.BootID) {
		t.Fatalf("creation did not persist the current boot ID: %q, want %q", record.BootID, bootID)
	}
	reopened, err := newStoreAt(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{record.ID, record.Name} {
		got, err := reopened.Get(context.Background(), ref)
		if err != nil || got.ID != record.ID || got.Name != "web" {
			t.Fatalf("get %s: %+v, %v", ref, got, err)
		}
	}
	cfg, err := reopened.Config(context.Background(), record.ID)
	if err != nil || strings.Join(cfg.Command, " ") != "/bin/echo hello" {
		t.Fatalf("configuration not preserved: %+v, %v", cfg, err)
	}
	for _, path := range []string{"config.json", "state.json", "container.log", ".lease"} {
		stat, err := os.Stat(filepath.Join(store.root, record.ID, path))
		if err != nil || stat.Mode().Perm() != 0600 {
			t.Fatalf("file %s must be private: %v, %v", path, stat, err)
		}
	}
	for _, ref := range []string{"../web", "web/../web", "/web", ""} {
		if _, err := store.Get(context.Background(), ref); err == nil {
			t.Fatalf("unsafe reference accepted: %q", ref)
		}
	}
	if _, err := store.Get(context.Background(), record.ID[:12]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("short ID must not resolve: %v", err)
	}
	auto := createTestRecord(t, store, "")
	if auto.Name != "mini-"+auto.ID[:12] {
		t.Fatalf("unexpected generated name: %q", auto.Name)
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 2 || records[0].ID != record.ID || records[1].ID != auto.ID {
		t.Fatalf("unexpected ordered listing: %+v, %v", records, err)
	}
}

func TestConcurrentNameCreationIsExclusive(t *testing.T) {
	store := testStore(t)
	const workers = 16
	var wg sync.WaitGroup
	errorsSeen := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Create(context.Background(), testConfig(), "web")
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	successes := 0
	for err := range errorsSeen {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrNameInUse) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected one successful named creation, got %d", successes)
	}
}

func TestUpdatesAreAtomicAndSerialized(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "web")
	const workers = 24
	var wg sync.WaitGroup
	errorsSeen := make(chan error, workers+1)
	stop := make(chan struct{})
	observerDone := make(chan struct{})
	go func() {
		defer close(observerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(filepath.Join(store.root, record.ID, "state.json"))
			var observed Record
			if err == nil {
				err = json.Unmarshal(data, &observed)
			}
			if err == nil {
				err = validateRecord(observed, record.ID)
			}
			if err != nil {
				errorsSeen <- fmt.Errorf("reader observed incomplete state: %w", err)
				return
			}
		}
	}()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsSeen <- store.Update(context.Background(), record.ID, func(record *Record) error {
				value := 0
				if record.Error != "" {
					var err error
					value, err = strconv.Atoi(record.Error)
					if err != nil {
						return err
					}
				}
				record.Error = strconv.Itoa(value + 1)
				return nil
			})
		}()
	}
	wg.Wait()
	close(stop)
	<-observerDone
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Get(context.Background(), record.ID)
	if err != nil || got.Error != strconv.Itoa(workers) {
		t.Fatalf("serialized updates lost changes: %+v, %v", got, err)
	}
	for _, update := range []func(*Record) error{
		func(r *Record) error { r.ID = strings.Repeat("b", 32); return nil },
		func(r *Record) error { r.Name = "renamed"; return nil },
		func(r *Record) error { r.Version = 2; return nil },
	} {
		if err := store.Update(context.Background(), record.ID, update); err == nil {
			t.Fatal("identity mutation accepted")
		}
	}
}

func TestLeaseBlocksRemovalAndDuplicateSupervision(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "web")
	if err := store.Remove(context.Background(), record.ID); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("running record removable: %v", err)
	}
	lease, err := store.AcquireLease(context.Background(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if second, err := store.AcquireLease(context.Background(), record.ID); !errors.Is(err, ErrBusy) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("duplicate supervision allowed: %v", err)
	}
	if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.State = StateExited; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), record.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("terminal record removed before supervisor released lease: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(context.Background(), record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed record still found: %v", err)
	}
	createTestRecord(t, store, "web")
}

func TestStoreLockHonorsCancellation(t *testing.T) {
	store := testStore(t)
	lock, err := store.lock(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := store.List(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting operation ignored deadline: %v", err)
	}
}

func TestMetadataRejectsUnsafeArtifacts(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "directory", "hardlink", "oversized", "public", "malformed", "wrong-id"} {
		t.Run(kind, func(t *testing.T) {
			store := testStore(t)
			record := createTestRecord(t, store, "web")
			id := record.ID
			path := filepath.Join(store.root, id, "state.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink("config.json", path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(store.root, record.ID, "config.json"), path); err != nil {
					t.Fatal(err)
				}
			case "oversized":
				file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(1024 * 1024 * 1024); err != nil {
					t.Fatal(err)
				}
				file.Close()
			case "public", "malformed":
				if err := os.WriteFile(path, []byte("invalid JSON"), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "public" {
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				}
			case "wrong-id":
				record.ID = strings.Repeat("b", 32)
				data, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Get(context.Background(), id); err == nil {
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
}

func TestCoordinationRejectsUnsafeArtifacts(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "hardlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			store := testStore(t)
			path := filepath.Join(store.root, ".lock")
			switch kind {
			case "symlink":
				if err := os.Symlink("missing", path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := unix.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				other := filepath.Join(store.root, "other")
				if err := os.WriteFile(other, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(other, path); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0620); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.List(context.Background()); err == nil {
				t.Fatal("unsafe coordination file accepted")
			}
		})
	}
}

func TestOversizedConfigurationDoesNotPublishContainer(t *testing.T) {
	store := testStore(t)
	cfg := testConfig()
	cfg.Env = []string{"PAYLOAD=" + strings.Repeat("x", int(maxConfigBytes))}
	if _, err := store.Create(context.Background(), cfg, "web"); err == nil {
		t.Fatal("oversized configuration accepted")
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			t.Fatalf("failed creation left artifact: %s", entry.Name())
		}
	}
}

func TestLogOpenRejectsLinksAndAppends(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "web")
	for _, text := range []string{"stdout\n", "stderr\n"} {
		file, err := store.OpenLog(context.Background(), record.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(text); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	file, err := store.OpenLog(context.Background(), record.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	path := filepath.Join(store.root, record.ID, "container.log")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "stdout\nstderr\n" {
		t.Fatalf("unexpected log: %q, %v", data, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("config.json", path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenLog(context.Background(), record.ID, true); err == nil {
		t.Fatal("symlink log accepted")
	}
}

func TestPrivateStorageRejectsUnsafeDirectories(t *testing.T) {
	for _, kind := range []string{"root-symlink", "root-public", "container-symlink", "container-public"} {
		t.Run(kind, func(t *testing.T) {
			store := testStore(t)
			record := createTestRecord(t, store, "web")
			path := store.root
			if strings.HasPrefix(kind, "container-") {
				path = filepath.Join(store.root, record.ID)
			}
			if strings.HasSuffix(kind, "symlink") {
				backup := path + "-original"
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(backup, path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Get(context.Background(), record.ID); err == nil {
				t.Fatal("unsafe storage directory accepted")
			}
		})
	}
}

func TestListingsIgnoreUnpublishedAndRemovedDirectories(t *testing.T) {
	store := testStore(t)
	record := createTestRecord(t, store, "web")
	for _, prefix := range []string{".create-", ".remove-"} {
		if err := os.Mkdir(filepath.Join(store.root, prefix+strings.Repeat("b", 32)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("incomplete transaction artifact affected listing: %+v, %v", records, err)
	}
}
