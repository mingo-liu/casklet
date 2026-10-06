package image

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIdentityTracksCopiedContent(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/outside/secret", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	first, size, err := Identity(context.Background(), root, "arm64")
	if err != nil || ValidateID(first) != nil || size != 8 {
		t.Fatalf("identity=%q size=%d error=%v", first, size, err)
	}
	if err := os.Chtimes(file, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	same, _, err := Identity(context.Background(), root, "arm64")
	if err != nil || same != first {
		t.Fatal("timestamps changed identity:", err)
	}
	other, _, err := Identity(context.Background(), root, "amd64")
	if err != nil || other == first {
		t.Fatal("architecture was omitted:", err)
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	other, _, err = Identity(context.Background(), root, "arm64")
	if err != nil || other == first {
		t.Fatal("modes were omitted:", err)
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	other, _, err = Identity(context.Background(), root, "arm64")
	if err != nil || other == first {
		t.Fatal("content was omitted:", err)
	}
	if err := os.WriteFile(file, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/other/secret", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	other, _, err = Identity(context.Background(), root, "arm64")
	if err != nil || other == first {
		t.Fatal("symlink target was omitted:", err)
	}
}

func TestValidateImageID(t *testing.T) {
	if err := ValidateID("sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../image", "/rootfs", "sha256:abc", strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64)} {
		if err := ValidateID(id); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
}
