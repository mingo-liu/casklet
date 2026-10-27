//go:build linux

package container

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mingo-liu/casklet/internal/ipc"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
)

func TestCopyRootEndpointFencesGenerationAndLifecycle(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("copy endpoint requires a root-owned client")
	}
	for _, kind := range []string{"running", "wrong-generation", "stopped", "missing-root"} {
		t.Run(kind, func(t *testing.T) {
			directory, err := os.MkdirTemp("", "ck-copy-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(directory)
			store, err := newStoreAt(filepath.Join(directory, "containers"))
			if err != nil {
				t.Fatal(err)
			}
			record := createTestRecord(t, store, "copy")
			if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.State = StateRunning; return nil }); err != nil {
				t.Fatal(err)
			}
			tree := t.TempDir()
			if err := os.WriteFile(filepath.Join(tree, "marker"), []byte("pinned root"), 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.Open(tree)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			server := newExecServer(store, record.ID, record.Generation)
			resources := containerruntime.ExecResources{Root: root}
			if kind == "missing-root" {
				resources.Root = nil
			}
			if err := server.Start(resources); err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			if kind == "stopped" {
				if err := store.Update(context.Background(), record.ID, func(r *Record) error { r.State = StateExited; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			conn, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: filepath.Join(store.root, record.ID, "exec.sock"), Net: "unixpacket"})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			generation := record.Generation
			if kind == "wrong-generation" {
				generation++
			}
			if err := ipc.Send(conn, execMessage{Version: 1, Kind: "copy-root", Generation: generation}, nil); err != nil {
				t.Fatal(err)
			}
			data, files, err := ipc.Receive(conn, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer ipc.CloseFiles(files)
			var response execMessage
			if err := ipc.Decode(data, &response); err != nil {
				t.Fatal(err)
			}
			if kind == "running" {
				if response.Kind != "copy-root" || response.Generation != record.Generation || len(files) != 1 {
					t.Fatalf("root response %+v files=%d", response, len(files))
				}
				info, err := files[0].Stat()
				expected, _ := root.Stat()
				if err != nil || !os.SameFile(info, expected) {
					t.Fatalf("wrong pinned root %v", err)
				}
			} else if len(files) != 0 || response.Kind != "result" || response.Code != 125 {
				t.Fatalf("granted invalid root %+v files=%d", response, len(files))
			}
		})
	}
}
