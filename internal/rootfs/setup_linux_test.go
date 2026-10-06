//go:build linux

package rootfs

import (
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReadOnlyRootFlagsPreserveMountRestrictions(t *testing.T) {
	mountInfo := "27 1 0:20 / /srv rw,relatime - ext4 /dev/vda rw\n" +
		"28 1 0:20 /private-root / rw,noexec,noatime,nodiratime,nosymfollow - ext4 /dev/vda rw\n" +
		"29 28 0:21 / /tmp rw,nosuid,nodev,noexec,relatime - tmpfs tmpfs rw\n"
	flags, err := readOnlyRootFlags(strings.NewReader(mountInfo))
	if err != nil {
		t.Fatal(err)
	}
	want := uintptr(unix.MS_REMOUNT | unix.MS_BIND | unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV |
		unix.MS_NOEXEC | unix.MS_NOATIME | unix.MS_NODIRATIME | unix.MS_NOSYMFOLLOW)
	if flags != want {
		t.Fatalf("root mount flags = %#x, want %#x", flags, want)
	}
	if flags&unix.MS_RELATIME != 0 {
		t.Fatal("timestamp behavior was inherited from a different mount")
	}
}

func TestReadOnlyRootFlagsRejectMissingOrMalformedRoot(t *testing.T) {
	for _, mountInfo := range []string{
		"",
		"28 1 0:20 / /tmp rw,relatime - tmpfs tmpfs rw\n",
		"28 1 0:20 / /\n",
	} {
		if _, err := readOnlyRootFlags(strings.NewReader(mountInfo)); err == nil {
			t.Fatalf("accepted mountinfo without a valid root entry: %q", mountInfo)
		}
	}
}

func TestBindRemountFlagsPreserveSourceRestrictions(t *testing.T) {
	info := "28 1 0:20 / / rw,relatime - ext4 /dev/vda rw\n" +
		"29 28 0:21 /source /data\\040dir ro,noexec,nosymfollow,noatime - ext4 /dev/vda rw\n"
	flags, err := bindRemountFlags(strings.NewReader(info), "/data dir", false)
	if err != nil {
		t.Fatal(err)
	}
	want := uintptr(unix.MS_REMOUNT | unix.MS_BIND | unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC | unix.MS_NOSYMFOLLOW | unix.MS_NOATIME)
	if flags != want {
		t.Fatalf("bind flags=%#x want=%#x", flags, want)
	}
}
