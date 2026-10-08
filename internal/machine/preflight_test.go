package machine

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mingo-liu/casklet/internal/config"
)

func TestLocalPreflightFailsBeforeLima(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "--rootfs", filepath.Join(t.TempDir(), "missing"), "--", "true"}, "rootfs template"},
		{[]string{"image", "import", filepath.Join(t.TempDir(), "missing")}, "rootfs template"},
		{[]string{"run", "--rootfs", BuiltinRootFS, "--mount", "type=bind,source=" + filepath.Join(t.TempDir(), "missing") + ",target=/data", "--", "true"}, "bind source"},
		{[]string{"run", "-p", "22:80", "--", "true"}, "reserved by Lima"},
		{[]string{"run", "-p", "192.0.2.1:49181:80", "--", "true"}, "host addresses"},
	} {
		code, err := Execute(context.Background(), Invocation{Args: tt.args}, os.Stdin, os.Stdout, os.Stderr)
		if code != 125 || err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "Lima is required") {
			t.Fatalf("%q: %d %v", tt.args, code, err)
		}
	}
}

func preflightTemplate(t *testing.T) string {
	t.Helper()
	root, err := sharedDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin", "proc", "dev", "tmp", "data"} {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Metadata validation needs only an ELF header; the fixture is never executed.
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

func TestCanonicalSourceOverlapFailsBeforeLimaRegardlessOfOptionOrder(t *testing.T) {
	root := preflightTemplate(t)
	rootLink := filepath.Join(t.TempDir(), "template-link")
	dataLink := filepath.Join(t.TempDir(), "data-link")
	if err := os.Symlink(root, rootLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "data"), dataLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, source := range []struct{ name, rootfs, bind string }{
		{"rootfs symlink", rootLink, filepath.Join(root, "data")},
		{"bind symlink", root, dataLink},
		{"both symlinks", rootLink, dataLink},
	} {
		t.Run(source.name, func(t *testing.T) {
			mount := config.BindMount{Source: source.bind, Target: "/data"}
			if err := config.ValidateMounts([]config.BindMount{mount}, source.rootfs); err != nil {
				t.Fatalf("fixture must pass lexical validation: %v", err)
			}
			for _, rootFirst := range []bool{true, false} {
				mountArg := "type=bind,source=" + source.bind + ",target=/data"
				options := []string{"--rootfs", source.rootfs, "--mount", mountArg}
				if !rootFirst {
					options = []string{"--mount", mountArg, "--rootfs", source.rootfs}
				}
				args := append(append([]string{"run"}, options...), "--", "true")
				code, err := Execute(context.Background(), Invocation{Args: args}, os.Stdin, os.Stdout, os.Stderr)
				if code != 125 || err == nil || !strings.Contains(err.Error(), "mount source and rootfs template must not overlap") {
					t.Fatalf("%q: %d %v", args, code, err)
				}
			}
		})
	}
}

func TestCanonicalPreflightAcceptsSeparateSourcesAndFinalBuiltinRootfs(t *testing.T) {
	root := preflightTemplate(t)
	for _, args := range [][]string{
		{"run", "--rootfs", root, "--mount", "type=bind,source=" + t.TempDir() + ",target=/data", "--", "true"},
		{"run", "--rootfs", root, "--mount", "type=bind,source=" + filepath.Join(root, "data") + ",target=/data", "--rootfs", BuiltinRootFS, "--", "true"},
	} {
		if err := localPreflight(args); err != nil {
			t.Fatalf("valid sources rejected for %q: %v", args, err)
		}
	}
}

func TestPortScannerSkipsOptionValuesAndWorkload(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--env", "-p=22:80", "--", "echo", "-p", "22:80"},
		{"run", "--rootfs", "--publish=22:80", "--", "true"},
	} {
		if err := checkPorts(args); err != nil {
			t.Fatalf("interpreted option value or workload as a port: %q %v", args, err)
		}
	}
}

func TestUnsharedPathsDoNotStartOrCreateMachine(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "calls")
			stub := filepath.Join(t.TempDir(), "limactl")
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> " + quote(log) + "\n"
			if existing {
				script += "printf '%s\\n' '{\"name\":\"casklet-runtime\",\"status\":\"Stopped\",\"config\":{\"mounts\":[]}}'\n"
			}
			if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			m := &Machine{lima: stub, directory: t.TempDir(), stderr: io.Discard}
			err := m.preflightShares(context.Background(), []string{"run", "--rootfs", t.TempDir(), "--", "true"})
			hint := "machine init --mount"
			if existing {
				hint = "machine share"
			}
			if err == nil || !strings.Contains(err.Error(), hint) {
				t.Fatalf("missing recovery: %v", err)
			}
			data, _ := os.ReadFile(log)
			if strings.Contains(string(data), "\nstart\n") || strings.Contains(string(data), "\nedit\n") {
				t.Fatalf("mutated VM: %s", data)
			}
		})
	}
}

func TestExportChecksDestinationWithoutCreatingDirectories(t *testing.T) {
	base, err := sharedDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(base, "new", "nested", "rootfs")
	if parent, err := exportParent(destination); err != nil || parent != base {
		t.Fatalf("parent: %s %v", parent, err)
	}
	if _, err := os.Stat(filepath.Join(base, "new")); !os.IsNotExist(err) {
		t.Fatal("created export directories during preflight")
	}
	if _, err := exportParent(base); err == nil {
		t.Fatal("accepted existing export destination")
	}
}
