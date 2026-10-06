package rootfs

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func template(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"bin", "proc", "dev", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// A minimal ELF header is sufficient to exercise metadata validation without
	// downloading or executing a real BusyBox binary on the development host.
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	machine := elf.EM_X86_64
	if runtime.GOARCH == "arm64" {
		machine = elf.EM_AARCH64
	}
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	if err := os.WriteFile(filepath.Join(root, "bin", "busybox"), header, 0755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidateCanonicalTemplate(t *testing.T) {
	root := template(t)
	link := filepath.Join(t.TempDir(), "template")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	got, err := Validate(link)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(root)
	if err != nil || got != want {
		t.Fatalf("Validate returned %q, want %q (resolve error %v)", got, want, err)
	}
}

func TestValidateRejectsInvalidTemplates(t *testing.T) {
	for _, name := range []string{"missing directory", "symlink mount target", "symlink bin", "nonexecutable BusyBox", "nonELF BusyBox", "wrong architecture", "dynamic BusyBox"} {
		t.Run(name, func(t *testing.T) {
			root := template(t)
			busybox := filepath.Join(root, "bin", "busybox")
			switch name {
			case "missing directory":
				must(t, os.Remove(filepath.Join(root, "proc")))
			case "symlink mount target":
				must(t, os.Remove(filepath.Join(root, "dev")))
				must(t, os.Symlink(t.TempDir(), filepath.Join(root, "dev")))
			case "symlink bin":
				must(t, os.Rename(filepath.Join(root, "bin"), filepath.Join(root, "real-bin")))
				must(t, os.Symlink("real-bin", filepath.Join(root, "bin")))
			case "nonexecutable BusyBox":
				must(t, os.Chmod(busybox, 0644))
			case "nonELF BusyBox":
				must(t, os.WriteFile(busybox, []byte("not ELF"), 0755))
			case "wrong architecture":
				data, err := os.ReadFile(busybox)
				must(t, err)
				binary.LittleEndian.PutUint16(data[18:], uint16(elf.EM_386))
				must(t, os.WriteFile(busybox, data, 0755))
			case "dynamic BusyBox":
				data, err := os.ReadFile(busybox)
				must(t, err)
				binary.LittleEndian.PutUint64(data[32:], 64)
				binary.LittleEndian.PutUint16(data[54:], 56)
				binary.LittleEndian.PutUint16(data[56:], 1)
				program := make([]byte, 56)
				binary.LittleEndian.PutUint32(program, uint32(elf.PT_INTERP))
				must(t, os.WriteFile(busybox, append(data, program...), 0755))
			}
			if _, err := Validate(root); err == nil {
				t.Fatal("invalid template was accepted")
			}
		})
	}
	for _, path := range []string{"", "/", filepath.Join(t.TempDir(), "missing")} {
		if _, err := Validate(path); err == nil {
			t.Fatalf("invalid root %q was accepted", path)
		}
	}
}

func TestCopyPreservesContentAndSymlinks(t *testing.T) {
	source := template(t)
	destination := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	must(t, os.WriteFile(outside, []byte("host secret"), 0600))
	must(t, os.WriteFile(filepath.Join(source, "data"), []byte("original"), 0640))
	must(t, os.Symlink(outside, filepath.Join(source, "external")))
	must(t, os.Symlink("/bin/busybox", filepath.Join(source, "bin", "sh")))
	must(t, os.Chmod(filepath.Join(source, "data"), 0640|os.ModeSetuid|os.ModeSetgid))
	must(t, Copy(context.Background(), source, destination))
	for _, name := range []string{"external", "bin/sh"} {
		original, err := os.Readlink(filepath.Join(source, name))
		must(t, err)
		copied, err := os.Readlink(filepath.Join(destination, name))
		must(t, err)
		if original != copied {
			t.Fatalf("link %s changed from %q to %q", name, original, copied)
		}
	}
	info, err := os.Stat(filepath.Join(destination, "data"))
	must(t, err)
	if info.Mode().Perm() != 0640 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Fatalf("unexpected file mode: %s", info.Mode())
	}
	must(t, os.WriteFile(filepath.Join(destination, "data"), []byte("changed"), 0640))
	data, err := os.ReadFile(filepath.Join(source, "data"))
	must(t, err)
	if string(data) != "original" {
		t.Fatal("copy changed the template")
	}
}

func TestCopyReadonlyDirectory(t *testing.T) {
	source, destination := template(t), t.TempDir()
	path := filepath.Join(source, "readonly")
	must(t, os.Mkdir(path, 0700))
	must(t, os.WriteFile(filepath.Join(path, "data"), []byte("data"), 0444))
	must(t, os.Chmod(path, 0555))
	t.Cleanup(func() { _ = os.Chmod(path, 0700) })
	must(t, Copy(context.Background(), source, destination))
	info, err := os.Stat(filepath.Join(destination, "readonly"))
	must(t, err)
	if info.Mode().Perm() != 0555 {
		t.Fatalf("readonly mode changed to %s", info.Mode())
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(destination, "readonly"), 0700) })
}

func TestCopyRejectsOverlappingAndNonemptyDestination(t *testing.T) {
	source := template(t)
	nested := filepath.Join(source, "destination")
	must(t, os.Mkdir(nested, 0700))
	for _, pair := range [][2]string{{source, source}, {source, nested}, {nested, source}, {"/", t.TempDir()}, {source, ""}} {
		if err := Copy(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatalf("invalid copy %q to %q accepted", pair[0], pair[1])
		}
	}
	nonempty := t.TempDir()
	must(t, os.WriteFile(filepath.Join(nonempty, "keep"), []byte("keep"), 0600))
	if err := Copy(context.Background(), source, nonempty); err == nil {
		t.Fatal("nonempty destination accepted")
	}
	link := filepath.Join(t.TempDir(), "destination")
	must(t, os.Symlink(t.TempDir(), link))
	if err := Copy(context.Background(), source, link); err == nil {
		t.Fatal("symlink destination accepted")
	}
}

func TestCopyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Copy(ctx, template(t), t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestCopyRejectsSymlinkDestinationWithSuffix(t *testing.T) {
	source := template(t)
	for _, suffix := range []string{"/", "/."} {
		t.Run(suffix, func(t *testing.T) {
			destination := t.TempDir()
			link := filepath.Join(t.TempDir(), "destination")
			must(t, os.Symlink(destination, link))
			if err := Copy(context.Background(), source, link+suffix); err == nil {
				t.Fatalf("symlink destination with suffix %q was accepted", suffix)
			}
			entries, err := os.ReadDir(destination)
			must(t, err)
			if len(entries) != 0 {
				t.Fatal("rejected destination was modified")
			}
		})
	}
}

func TestCopyDirectorySpecialModes(t *testing.T) {
	source, destination := template(t), t.TempDir()
	path := filepath.Join(source, "shared")
	must(t, os.Mkdir(path, 0700))
	must(t, os.Chmod(path, 0777|os.ModeSticky|os.ModeSetuid|os.ModeSetgid))
	must(t, Copy(context.Background(), source, destination))
	info, err := os.Stat(filepath.Join(destination, "shared"))
	must(t, err)
	if info.Mode().Perm() != 0777 || info.Mode()&os.ModeSticky == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Fatalf("unexpected shared-directory permissions: %s", info.Mode())
	}
}

func TestCopyMultipleChunks(t *testing.T) {
	source, destination := template(t), t.TempDir()
	// Distinct nonzero bytes exercise multiple reads and the final short read.
	data := make([]byte, 96*1024+17)
	for i := range data {
		data[i] = byte(i%251 + 1)
	}
	must(t, os.WriteFile(filepath.Join(source, "large"), data, 0644))
	must(t, os.WriteFile(filepath.Join(source, "small"), []byte("short"), 0644))
	must(t, Copy(context.Background(), source, destination))
	for name, want := range map[string][]byte{"large": data, "small": []byte("short")} {
		got, err := os.ReadFile(filepath.Join(destination, name))
		must(t, err)
		if !bytes.Equal(got, want) {
			t.Fatalf("copied file %s has incorrect content", name)
		}
	}
}

func BenchmarkCopyManyFiles(b *testing.B) {
	source, parent := b.TempDir(), b.TempDir()
	data := make([]byte, 1024)
	for i := 0; i < 128; i++ {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("file-%03d", i)), data, 0644); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		destination, err := os.MkdirTemp(parent, "copy-")
		if err != nil {
			b.Fatal(err)
		}
		if err := Copy(context.Background(), source, destination); err != nil {
			b.Fatal(err)
		}
		if err := os.RemoveAll(destination); err != nil {
			b.Fatal(err)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
