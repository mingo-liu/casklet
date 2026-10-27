//go:build linux

package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mingo-liu/casklet/internal/copyfs"
	"github.com/mingo-liu/casklet/internal/image"
	"github.com/mingo-liu/casklet/internal/ipc"
	"github.com/mingo-liu/casklet/internal/rootfs"
	containerruntime "github.com/mingo-liu/casklet/internal/runtime"
	"golang.org/x/sys/unix"
)

type copyControl struct {
	Path    string
	Upload  bool
	Overlay *rootfs.Overlay
}

func copyPathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Copy holds the immutable container's lifecycle lock through the stream. Live
// roots come from the authenticated supervisor, never a persisted numeric PID.
func Copy(ctx context.Context, ref, name string, upload bool, stdin, stdout, stderr *os.File) error {
	clean := path.Clean(name)
	if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return errors.New("copy requires a path below the container root")
	}
	for _, reserved := range []string{"proc", "sys", "dev"} {
		if copyPathsOverlap(clean, reserved) {
			return errors.New("copy cannot access /proc, /sys, or /dev")
		}
	}
	store, record, operation, err := lockReference(ctx, ref)
	if err != nil {
		return err
	}
	defer operation.Close()
	record, err = refreshRecord(ctx, store, record, false)
	if err != nil {
		return err
	}
	cfg, err := store.Config(ctx, record.ID)
	if err != nil {
		return err
	}
	control := copyControl{Path: name, Upload: upload}
	var rootFile *os.File
	if record.State == StateRunning {
		rootFile, err = liveCopyRoot(ctx, store, record)
		if err != nil {
			return err
		}
		defer rootFile.Close()
	} else {
		if !record.Terminal() {
			return errors.New("copy requires a running or stopped container")
		}
		if upload && cfg.ReadOnly {
			return errors.New("copy cannot write a read-only container root")
		}
		for _, mount := range cfg.Mounts {
			if copyPathsOverlap(clean, strings.TrimPrefix(mount.Target, "/")) {
				return errors.New("copy cannot access an unmounted bind or volume in a stopped container; start it first")
			}
		}
		if copyPathsOverlap(clean, "tmp") {
			return errors.New("copy cannot access stopped temporary storage; start the container first")
		}
		if record.RunPath != "" {
			if err := containerruntime.RecoverRun(ctx, record.RunPath); err != nil {
				return err
			}
		}
		lease, err := store.AcquireLease(ctx, record.ID)
		if err != nil {
			return err
		}
		defer lease.Close()
		retained, err := store.RootFS(ctx, record.ID)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(retained); err == nil {
			if err := rootfs.CheckUnmounted(retained); err != nil {
				return err
			}
			rootFile, err = os.Open(retained)
			if err != nil {
				return err
			}
			defer rootFile.Close()
		} else if errors.Is(err, os.ErrNotExist) {
			storage := rootfs.OverlayStoragePath(retained)
			id, overlay, err := rootfs.ReadOverlayImageID(storage)
			if err != nil {
				return err
			}
			if !overlay || id != cfg.Image {
				return errors.New("container has no complete retained filesystem")
			}
			images, err := image.OpenStore()
			if err != nil {
				return err
			}
			_, lower, lease, err := images.Acquire(ctx, id)
			if err != nil {
				return err
			}
			defer lease.Close()
			target, err := os.MkdirTemp("", "casklet-copy-*")
			if err != nil {
				return err
			}
			defer os.Remove(target)
			control.Overlay = &rootfs.Overlay{Lower: lower, Storage: storage, Target: target}
		} else {
			return err
		}
	}
	data, err := os.CreateTemp("", "casklet-copy-control-*")
	if err != nil {
		return err
	}
	defer data.Close()
	defer os.Remove(data.Name())
	if err := json.NewEncoder(data).Encode(control); err != nil {
		return err
	}
	if _, err := data.Seek(0, 0); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, "__copy")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.ExtraFiles = []*os.File{data}
	if rootFile != nil {
		cmd.ExtraFiles = append(cmd.ExtraFiles, rootFile)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Unshareflags: unix.CLONE_NEWNS, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container copy: %w", err)
	}
	return nil
}

func liveCopyRoot(ctx context.Context, store *Store, record Record) (*os.File, error) {
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := inspectUnit(startup, record.ID, record.Generation)
	if err != nil {
		return nil, err
	}
	if !status.live() || status.MainPID <= 0 {
		return nil, errors.New("container supervisor is unavailable")
	}
	endpoint := filepath.Join(store.root, record.ID, "exec.sock")
	var stat unix.Stat_t
	if err := unix.Lstat(endpoint, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != 0 || stat.Mode&0777 != 0600 {
		return nil, errors.New("unsafe container copy endpoint")
	}
	connection, err := (&net.Dialer{}).DialContext(startup, "unixpacket", endpoint)
	if err != nil {
		return nil, err
	}
	conn := connection.(*net.UnixConn)
	defer conn.Close()
	peer, err := ipc.Peer(conn)
	if err != nil || peer.Uid != 0 || int(peer.Pid) != status.MainPID {
		return nil, errors.New("unexpected container copy supervisor")
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	stop := context.AfterFunc(startup, func() { conn.Close() })
	defer stop()
	if err := ipc.Send(conn, execMessage{Version: 1, Kind: "copy-root", Generation: record.Generation}, nil); err != nil {
		return nil, err
	}
	data, files, err := ipc.Receive(conn, 1)
	if err != nil {
		return nil, err
	}
	var response execMessage
	if ipc.Decode(data, &response) != nil || response.Version != 1 || response.Kind != "copy-root" || response.Generation != record.Generation || response.Options != nil || response.Signal != 0 || response.Code != 0 || response.Error != "" || len(files) != 1 {
		ipc.CloseFiles(files)
		return nil, errors.New("supervisor does not support file copying; restart the container and retry")
	}
	info, err := files[0].Stat()
	if err != nil || !info.IsDir() {
		ipc.CloseFiles(files)
		return nil, errors.New("invalid container copy root")
	}
	return files[0], nil
}

// CopyInit runs in a new private mount namespace. A stopped OverlayFS root is
// mounted only here; process exit also disposes of its namespace on abrupt loss.
func CopyInit() int {
	err := copyInit()
	if err != nil {
		fmt.Fprintln(os.Stderr, "casklet:", err)
		return 125
	}
	return 0
}

func copyInit() error {
	if os.Geteuid() != 0 {
		return errors.New("container copying requires the Linux engine as root")
	}
	data := os.NewFile(3, "copy-control")
	defer data.Close()
	decoder := json.NewDecoder(data)
	decoder.DisallowUnknownFields()
	var control copyControl
	if err := decoder.Decode(&control); err != nil {
		return err
	}
	rootPath := "/proc/self/fd/4"
	if control.Overlay != nil {
		if err := rootfs.MountOverlay(*control.Overlay); err != nil {
			return err
		}
		rootPath = control.Overlay.Target
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer cancel()
	stream := os.Stdout
	if control.Upload {
		stream = os.Stdin
	}
	// Wrap inherited streams in Go's poller so a blocked copy can be canceled.
	fd, err := unix.FcntlInt(stream.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return err
	}
	file := os.NewFile(uintptr(fd), "copy-stream")
	defer file.Close()
	stop := context.AfterFunc(ctx, func() { file.Close() })
	defer stop()
	if control.Upload {
		return copyfs.Import(ctx, root, control.Path, file)
	}
	return copyfs.Export(ctx, root, control.Path, file)
}
