package copyfs

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	name := t.TempDir()
	root, err := os.OpenRoot(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, name
}
func archive(t *testing.T, root *os.Root, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Export(context.Background(), root, name, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestCopyRoundTripAndDestinationRules(t *testing.T) {
	src, srcPath := testRoot(t)
	dst, dstPath := testRoot(t)
	if err := os.MkdirAll(filepath.Join(srcPath, "tree", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcPath, "tree", "nested", "binary"), []byte("a\x00b\xff"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := src.Symlink("nested/binary", "tree/link"); err != nil {
		t.Fatal(err)
	}
	data := archive(t, src, "tree")
	if err := Import(context.Background(), dst, "renamed", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dstPath, "renamed", "nested", "binary"))
	if err != nil || string(got) != "a\x00b\xff" {
		t.Fatalf("binary %q %v", got, err)
	}
	if link, err := dst.Readlink("renamed/link"); err != nil || link != "nested/binary" {
		t.Fatalf("link %q %v", link, err)
	}
	info, _ := dst.Stat("renamed/nested/binary")
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode %v", info.Mode())
	}
	if err := dst.Mkdir("existing", 0755); err != nil {
		t.Fatal(err)
	}
	if err := Import(context.Background(), dst, "existing", bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Stat("existing/tree/nested/binary"); err != nil {
		t.Fatal(err)
	}
	if err := Import(context.Background(), dst, "existing", bytes.NewReader(archive(t, src, "tree/."))); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Stat("existing/nested/binary"); err != nil {
		t.Fatal(err)
	}
	if err := Import(context.Background(), dst, "missing-parent/value", bytes.NewReader(data)); err == nil {
		t.Fatal("created missing parents")
	}
	if err := Import(context.Background(), dst, "missing", bytes.NewReader(archive(t, src, "tree/."))); err == nil {
		t.Fatal("accepted contents into missing destination")
	}
	file := archive(t, src, "tree/nested/binary")
	if err := Import(context.Background(), dst, "renamed/nested/binary", bytes.NewReader(file)); err != nil {
		t.Fatal(err)
	}
	if err := Import(context.Background(), dst, "renamed/nested/binary", bytes.NewReader(data)); err == nil {
		t.Fatal("replaced file with directory")
	}
	if err := Import(context.Background(), dst, "existing", bytes.NewReader(file)); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.Stat("existing/binary"); err != nil {
		t.Fatal(err)
	}
}
func TestCopyRejectsDestinationSymlinksAndPreservesOutsideFiles(t *testing.T) {
	src, sp := testRoot(t)
	dst, dp := testRoot(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(sp, "file"), []byte("new"), 0600)
	sentinel := filepath.Join(outside, "sentinel")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	for _, link := range []string{outside, "../" + filepath.Base(outside)} {
		os.Remove(filepath.Join(dp, "link"))
		if err := dst.Symlink(link, "link"); err != nil {
			t.Fatal(err)
		}
		for _, dest := range []string{"link", "link/sentinel"} {
			if err := Import(context.Background(), dst, dest, bytes.NewReader(archive(t, src, "file"))); err == nil {
				t.Fatalf("followed %s", dest)
			}
		}
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "keep" {
		t.Fatal("changed outside file")
	}
	if err := src.Symlink(outside, "outside"); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), src, "outside/sentinel", io.Discard); err == nil {
		t.Fatal("export escaped root")
	}
	if err := Import(context.Background(), dst, "copied-link", bytes.NewReader(archive(t, src, "outside"))); err != nil {
		t.Fatal(err)
	}
	if link, _ := dst.Readlink("copied-link"); link != outside {
		t.Fatalf("link %s", link)
	}
}
func tarData(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			tw.Write(bytes.Repeat([]byte{'x'}, int(h.Size)))
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestCopyRejectsMalformedTrees(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []*tar.Header
	}{
		{"traversal", []*tar.Header{{Name: "../escape", Mode: 0600, Typeflag: tar.TypeReg}}},
		{"absolute", []*tar.Header{{Name: "/escape", Mode: 0600, Typeflag: tar.TypeReg}}},
		{"hardlink", []*tar.Header{{Name: "file", Mode: 0600, Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}}},
		{"fifo", []*tar.Header{{Name: "file", Mode: 0600, Typeflag: tar.TypeFifo}}},
		{"duplicate", []*tar.Header{{Name: "tree", Mode: 0700, Typeflag: tar.TypeDir}, {Name: "tree", Mode: 0700, Typeflag: tar.TypeDir}}},
		{"sibling", []*tar.Header{{Name: "tree", Mode: 0700, Typeflag: tar.TypeDir}, {Name: "outside", Mode: 0600, Typeflag: tar.TypeReg}}},
		{"child-of-file", []*tar.Header{{Name: "tree", Mode: 0600, Typeflag: tar.TypeReg}, {Name: "tree/file", Mode: 0600, Typeflag: tar.TypeReg}}},
		{"missing-parent", []*tar.Header{{Name: "tree", Mode: 0700, Typeflag: tar.TypeDir}, {Name: "tree/missing/file", Mode: 0600, Typeflag: tar.TypeReg}}},
		{"symlink-parent", []*tar.Header{{Name: "tree", Mode: 0700, Typeflag: tar.TypeDir}, {Name: "tree/link", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "."}, {Name: "tree/link/file", Mode: 0600, Typeflag: tar.TypeReg}}},
		{"special-mode", []*tar.Header{{Name: "file", Mode: 04755, Typeflag: tar.TypeReg}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := testRoot(t)
			if err := Import(context.Background(), root, "result", bytes.NewReader(tarData(t, tc.headers...))); err == nil {
				t.Fatal("accepted invalid archive")
			}
		})
	}
}
func TestCopyFailureLeavesOldFileAndRemovesStaging(t *testing.T) {
	root, rp := testRoot(t)
	os.WriteFile(filepath.Join(rp, "value"), []byte("old"), 0600)
	data := tarData(t, &tar.Header{Name: "source", Typeflag: tar.TypeReg, Size: 4096, Mode: 0644})
	if err := Import(context.Background(), root, "value", bytes.NewReader(data[:600])); err == nil {
		t.Fatal("accepted truncation")
	}
	if got, _ := os.ReadFile(filepath.Join(rp, "value")); string(got) != "old" {
		t.Fatalf("old file replaced: %q", got)
	}
	entries, _ := os.ReadDir(rp)
	if len(entries) != 1 {
		t.Fatalf("leaked stages: %v", entries)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Import(ctx, root, "value", bytes.NewReader(data)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if err := Export(ctx, root, "value", io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel export: %v", err)
	}
}
func TestCopyLimitsAndOutputFailure(t *testing.T) {
	root, rp := testRoot(t)
	os.WriteFile(filepath.Join(rp, "value"), []byte("abc"), 0600)
	if err := Export(context.Background(), root, "value", failingWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	s := stream{ctx: context.Background(), w: io.Discard, n: MaxBytes}
	if _, err := s.Write([]byte{1}); err == nil {
		t.Fatal("no byte limit")
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "value", Mode: 0600, Typeflag: tar.TypeReg, Size: MaxBytes + 1})
	if err := Import(context.Background(), root, "out", bytes.NewReader(buf.Bytes())); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("oversize: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestCopyRequiresCompleteArchiveAndRejectsTrailingData(t *testing.T) {
	data := tarData(t, &tar.Header{Name: "value", Mode: 0600, Typeflag: tar.TypeReg, Size: 512})
	for _, input := range [][]byte{data[:1024], data[:len(data)-512], append(append([]byte(nil), data...), 1)} {
		root, _ := testRoot(t)
		if err := Import(context.Background(), root, "result", bytes.NewReader(input)); err == nil {
			t.Fatal("accepted malformed termination")
		}
	}
	root, _ := testRoot(t)
	if err := Import(context.Background(), root, "result", bytes.NewReader(append(data, make([]byte, 1024)...))); err != nil {
		t.Fatal(err)
	}
}

func TestCopyDestinationPolicyRunsBeforeMutation(t *testing.T) {
	root, dir := testRoot(t)
	data := tarData(t, &tar.Header{Name: "protected", Mode: 0600, Typeflag: tar.TypeReg, Size: 5})
	denied := errors.New("protected receiving path")
	err := ImportChecked(context.Background(), root, ".", bytes.NewReader(data), func(target string) error {
		if target != "protected" {
			t.Fatalf("policy target %s", target)
		}
		return denied
	})
	if !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("mutated denied destination")
	}
}
