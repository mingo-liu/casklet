//go:build linux

package volume

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArchiveRoundTripAndExclusiveLease(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	s.Create(ctx, "source")
	data := filepath.Join(s.root, "source", "data")
	os.Mkdir(filepath.Join(data, "private"), 0700)
	filename := filepath.Join(data, "private", "value")
	os.WriteFile(filename, []byte("persistent\x00data"), 0640)
	timestamp := time.Unix(1600000000, 0)
	os.Chtimes(filename, timestamp, timestamp)
	os.Link(filename, filepath.Join(data, "hard"))
	os.Symlink("/outside/never-read", filepath.Join(data, "absolute"))
	os.Symlink("private/value", filepath.Join(data, "relative"))
	lease, err := s.Acquire(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := s.Export(ctx, "source", &archive); !errors.Is(err, ErrInUse) {
		t.Fatalf("live export: %v", err)
	}
	lease.Close()
	if err := s.Export(ctx, "source", &archive); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Restore(ctx, "copy", bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(s.root, "copy", "data")
	got, err := os.ReadFile(filepath.Join(restored, "private", "value"))
	if err != nil || string(got) != "persistent\x00data" {
		t.Fatalf("content: %q %v", got, err)
	}
	info, _ := os.Stat(filepath.Join(restored, "private", "value"))
	if info.Mode().Perm() != 0640 || !info.ModTime().Equal(timestamp) {
		t.Fatalf("metadata: %v", info)
	}
	linked, _ := os.Stat(filepath.Join(restored, "hard"))
	if !os.SameFile(info, linked) {
		t.Fatal("hardlink lost")
	}
	for _, name := range []string{"absolute", "relative"} {
		before, _ := os.Readlink(filepath.Join(data, name))
		after, _ := os.Readlink(filepath.Join(restored, name))
		if before != after {
			t.Fatal("symlink changed")
		}
	}
	if _, err := s.Restore(ctx, "copy", bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("overwrote existing volume")
	}
	for _, source := range [][]byte{nil, archive.Bytes()[:len(archive.Bytes())-512], append(append([]byte(nil), archive.Bytes()...), byte(1))} {
		if _, err := s.Restore(ctx, "broken", bytes.NewReader(source)); err == nil {
			t.Fatal("accepted incomplete archive")
		}
		if _, err := s.Inspect(ctx, "broken"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("published failed restore: %v", err)
		}
	}
}

func tarFixture(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	writer := tar.NewWriter(&b)
	for _, h := range headers {
		h.Uid = os.Geteuid()
		h.Gid = os.Getgid()
		h.ModTime = time.Unix(1600000000, 0)
		if err := writer.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRestoreRejectsUnsafeArchiveAndCleansStaging(t *testing.T) {
	cases := [][]*tar.Header{
		{{Name: "../escape", Typeflag: tar.TypeReg}}, {{Name: "/escape", Typeflag: tar.TypeReg}}, {{Name: "a/../escape", Typeflag: tar.TypeReg}},
		{{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}, {Name: "a/escape", Typeflag: tar.TypeReg}},
		{{Name: "a", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}},
		{{Name: "fifo", Typeflag: tar.TypeFifo}}, {{Name: "device", Typeflag: tar.TypeChar}},
		{{Name: "link", Typeflag: tar.TypeLink, Linkname: "../escape"}}, {{Name: "link", Typeflag: tar.TypeLink, Linkname: "missing"}},
		{{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "value"}, {Name: "value", Typeflag: tar.TypeReg}, {Name: "hard", Typeflag: tar.TypeLink, Linkname: "sym"}},
	}
	s := testStore(t)
	for _, headers := range cases {
		if _, err := s.Restore(context.Background(), "broken", bytes.NewReader(tarFixture(t, headers...))); err == nil {
			t.Fatalf("accepted %+v", headers)
		}
		entries, _ := os.ReadDir(s.root)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".create-") {
				t.Fatal("staging leaked")
			}
		}
	}
	// Forward hardlinks are valid and resolve without following symlinks.
	archive := tarFixture(t, &tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "value"}, &tar.Header{Name: "value", Typeflag: tar.TypeReg})
	if _, err := s.Restore(context.Background(), "forward", bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRestoreSurvivesRecoveryAndDoesNotBlockOtherVolumes(t *testing.T) {
	s := testStore(t)
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { _, err := s.Restore(context.Background(), "copy", reader); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		entries, _ := os.ReadDir(s.root)
		found := false
		for _, entry := range entries {
			found = found || strings.HasPrefix(entry.Name(), ".create-")
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restore did not stage")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := s.Create(context.Background(), "other"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	archive := tarFixture(t, &tar.Header{Name: "value", Typeflag: tar.TypeReg})
	writer.Write(archive)
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Restore(ctx, "canceled", bytes.NewReader(archive)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestExportRejectsSpecialFiles(t *testing.T) {
	s := testStore(t)
	s.Create(context.Background(), "source")
	syscall.Mkfifo(filepath.Join(s.root, "source", "data", "fifo"), 0600)
	if err := s.Export(context.Background(), "source", io.Discard); err == nil {
		t.Fatal("exported FIFO")
	}
}
