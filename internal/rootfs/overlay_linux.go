//go:build linux

package rootfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const overlayMarker = ".casklet-overlay-v1.json"

// OverlayStoragePath is engine-owned storage separate from a legacy full root.
// A file inside an arbitrary directory root can never select this backend.
func OverlayStoragePath(retainedRoot string) string { return retainedRoot + ".overlay" }

type overlayRecord struct {
	Version int    `json:"version"`
	Image   string `json:"image"`
}

// Overlay describes storage used by one init's private mount namespace. The
// image lease must remain held until that namespace and its exec handles close.
type Overlay struct {
	Lower   string `json:"lower"`
	Storage string `json:"storage"`
	Target  string `json:"target"`
}

func validOverlayID(id string) bool {
	if !strings.HasPrefix(id, "sha256:") || len(id) != 71 {
		return false
	}
	for _, c := range id[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// ReadOverlayImageID distinguishes retained overlay storage from legacy copied
// roots. It validates the private envelope before callers reacquire the lower
// image. The upper filesystem may have arbitrary image ownership and modes.
func ReadOverlayImageID(storage string) (string, bool, error) {
	fd, err := unix.Open(filepath.Join(storage, overlayMarker), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	file := os.NewFile(uintptr(fd), "overlay-record")
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", false, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || stat.Nlink != 1 || stat.Size > 1024 {
		return "", false, errors.New("unsafe retained overlay record")
	}
	if err := privateOverlayDirectory(storage); err != nil {
		return "", false, err
	}
	decoder := json.NewDecoder(io.LimitReader(file, 1025))
	decoder.DisallowUnknownFields()
	var record overlayRecord
	if err := decoder.Decode(&record); err != nil {
		return "", false, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return "", false, errors.New("invalid retained overlay record suffix")
	}
	if record.Version != 1 || !validOverlayID(record.Image) {
		return "", false, errors.New("invalid retained overlay image identity")
	}
	for _, name := range []string{"upper", "work"} {
		if err := realDirectory(filepath.Join(storage, name)); err != nil {
			return "", false, err
		}
	}
	if err := privateOverlayDirectory(filepath.Join(storage, "work")); err != nil {
		return "", false, err
	}
	if err := CheckUnmounted(storage); err != nil {
		return "", false, err
	}
	return record.Image, true, nil
}

func privateOverlayDirectory(path string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		return errors.New("overlay storage must be a private directory owned by the engine")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("overlay storage path contains a symlink")
	}
	return nil
}

// CreateOverlayStorage initializes an empty private directory without copying
// image contents. The caller syncs and atomically publishes retained storage.
func CreateOverlayStorage(ctx context.Context, lower, storage, image string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validOverlayID(image) {
		return errors.New("overlay storage requires an immutable image identity")
	}
	if err := privateOverlayDirectory(storage); err != nil {
		return err
	}
	entries, err := os.ReadDir(storage)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("new overlay storage must be empty")
	}
	if _, err := Validate(lower); err != nil {
		return err
	}
	if err := CheckUnmounted(lower); err != nil {
		return err
	}
	if within(lower, storage) || within(storage, lower) {
		return errors.New("overlay lower and storage must not overlap")
	}
	for _, name := range []string{"upper", "work"} {
		if err := os.Mkdir(filepath.Join(storage, name), 0700); err != nil {
			return err
		}
	}
	info, err := os.Stat(lower)
	if err != nil {
		return err
	}
	uid, gid := Ownership(info)
	if err := os.Chown(filepath.Join(storage, "upper"), int(uid), int(gid)); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(storage, "upper"), info.Mode()&^(os.ModeSetuid|os.ModeSetgid)); err != nil {
		return err
	}
	data, err := json.Marshal(overlayRecord{Version: 1, Image: image})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(storage, overlayMarker), data, 0600)
}

// MountOverlay runs only inside init's new mount namespace. Pinned descriptors
// keep option values independent of path punctuation and filesystem lookups.
// Making propagation private first ensures no overlay reaches the guest host.
func MountOverlay(overlay Overlay) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make overlay mounts private: %w", err)
	}
	if _, ready, err := ReadOverlayImageID(overlay.Storage); err != nil || !ready {
		if err == nil {
			err = errors.New("missing overlay storage record")
		}
		return err
	}
	if err := CheckUnmounted(overlay.Lower); err != nil {
		return err
	}
	if err := CheckUnmounted(overlay.Target); err != nil {
		return err
	}
	var files []*os.File
	defer func() { closeMountSources(files) }()
	for _, path := range []string{overlay.Lower, filepath.Join(overlay.Storage, "upper"), filepath.Join(overlay.Storage, "work"), overlay.Target} {
		resolved, err := directory(path)
		if err != nil || resolved != path || resolved == "/" {
			return errors.New("overlay mount requires canonical directories")
		}
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		files = append(files, os.NewFile(uintptr(fd), "overlay-directory"))
	}
	if within(overlay.Lower, overlay.Target) || within(overlay.Target, overlay.Lower) || within(overlay.Storage, overlay.Target) || within(overlay.Target, overlay.Storage) {
		return errors.New("overlay mount paths must not overlap")
	}
	options := fmt.Sprintf("lowerdir=/proc/self/fd/%d,upperdir=/proc/self/fd/%d,workdir=/proc/self/fd/%d,index=off,redirect_dir=nofollow,metacopy=off", files[0].Fd(), files[1].Fd(), files[2].Fd())
	if err := unix.Mount("overlay", fmt.Sprintf("/proc/self/fd/%d", files[3].Fd()), "overlay", unix.MS_NOSUID|unix.MS_NODEV, options); err != nil {
		return fmt.Errorf("mount image copy-on-write filesystem (OverlayFS support is required): %w", err)
	}
	return nil
}
