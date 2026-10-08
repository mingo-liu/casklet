//go:build linux

package image

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := newStoreAt(filepath.Join(t.TempDir(), "images"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func testTemplate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"bin", "proc", "dev", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	machine := elf.EM_AARCH64
	if runtime.GOARCH == "amd64" {
		machine = elf.EM_X86_64
	}
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	if err := os.WriteFile(filepath.Join(root, "bin/busybox"), header, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/busybox", filepath.Join(root, "bin/sh")); err != nil {
		t.Fatal(err)
	}
	return root
}
func unused(context.Context, string) (bool, error) { return false, nil }

func TestStoreImportDeduplicatesAndPreservesSource(t *testing.T) {
	store := testStore(t)
	source := testTemplate(t)
	ctx := context.Background()
	record, err := store.Import(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.Import(ctx, source)
	if err != nil || again != record {
		t.Fatalf("duplicate import=%+v error=%v", again, err)
	}
	records, err := store.List(ctx)
	if err != nil || len(records) != 1 || records[0] != record {
		t.Fatalf("list=%v error=%v", records, err)
	}
	first, tree, lease, err := store.Acquire(ctx, record.ID)
	if err != nil || first != record {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tree, store.root+"/") {
		t.Fatal("rootfs escaped store")
	}
	if err := store.Remove(ctx, record.ID, unused); !errors.Is(err, ErrInUse) {
		t.Fatalf("removed leased image: %v", err)
	}
	lease.Close()
	if err := store.Remove(ctx, record.ID, func(context.Context, string) (bool, error) { return true, nil }); !errors.Is(err, ErrInUse) {
		t.Fatalf("removed referenced image: %v", err)
	}
	checkError := errors.New("reference storage unavailable")
	if err := store.Remove(ctx, record.ID, func(context.Context, string) (bool, error) { return false, checkError }); !errors.Is(err, checkError) {
		t.Fatalf("reference error was ignored: %v", err)
	}
	if err := store.Remove(ctx, record.ID, nil); err == nil {
		t.Fatal("removed without checking references")
	}
	if err := store.Remove(ctx, record.ID, unused); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(source, "bin/busybox")); err != nil {
		t.Fatal("source removed:", err)
	}
	if _, _, _, err := store.Acquire(ctx, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed image lookup: %v", err)
	}
}

func TestStoreImportRejectsUnsafePathsAndRollsBack(t *testing.T) {
	store := testStore(t)
	source := testTemplate(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/", "/proc", "/var/lib", store.root, link, link + "/bin", source + "/missing"} {
		if _, err := store.Import(context.Background(), path); err == nil {
			t.Errorf("accepted %s", path)
		}
	}
	if err := unix.Mkfifo(filepath.Join(source, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(context.Background(), source); err == nil {
		t.Fatal("import accepted FIFO")
	}
	if err := os.Remove(filepath.Join(source, "fifo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(source, "proc")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc", filepath.Join(source, "proc")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(context.Background(), source); err == nil {
		t.Fatal("import accepted invalid rootfs")
	}
	records, err := store.List(context.Background())
	if err != nil || len(records) != 0 {
		t.Fatalf("failed import published images: %v %v", records, err)
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			t.Errorf("failed import left %s", entry.Name())
		}
	}
}

func TestStoreConcurrentImportAndReaders(t *testing.T) {
	store := testStore(t)
	source := testTemplate(t)
	ctx := context.Background()
	results := make(chan Record, 4)
	failures := make(chan error, 4)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			record, err := store.Import(ctx, source)
			if err != nil {
				failures <- err
			} else {
				results <- record
			}
		}()
	}
	workers.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	id := ""
	for record := range results {
		if id != "" && id != record.ID {
			t.Fatal("concurrent imports changed identity")
		}
		id = record.ID
	}
	_, _, lease1, err := store.Acquire(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease1.Close()
	_, _, lease2, err := store.Acquire(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer lease2.Close()
	if err := store.Remove(ctx, id, unused); !errors.Is(err, ErrInUse) {
		t.Fatalf("concurrent readers not protected: %v", err)
	}
}

func TestStoreCorruptionAndCanceledOperations(t *testing.T) {
	store := testStore(t)
	source := testTemplate(t)
	ctx := context.Background()
	record, err := store.Import(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.path(record.ID), "rootfs", "extra"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Acquire(ctx, record.ID); err == nil {
		t.Fatal("tampered content accepted")
	}
	if _, err := store.Import(ctx, source); err == nil {
		t.Fatal("duplicate import accepted tampered image")
	}
	locked, err := store.lock(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := store.List(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait ignored cancellation: %v", err)
	}
	locked.Close()
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := store.Import(canceled, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("import ignored cancellation: %v", err)
	}
}

func TestStoreRejectsSymlinkArtifactsAndRecoversStaging(t *testing.T) {
	store := testStore(t)
	source := testTemplate(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.root, ".import-abandoned"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(store.root, ".import-abandoned", "external")); err != nil {
		t.Fatal(err)
	}
	record, err := store.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.root, ".import-abandoned")); !os.IsNotExist(err) {
		t.Fatalf("staging not reclaimed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("recovery followed symlink")
	}
	file := filepath.Join(store.path(record.ID), "image.json")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "keep"), file); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); err == nil {
		t.Fatal("metadata symlink accepted")
	}
}
