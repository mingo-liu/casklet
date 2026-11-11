//go:build linux || darwin

package rootfs

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
)

type ownershipInfo struct {
	fs.FileInfo

	system any
}

func (info ownershipInfo) Sys() any { return info.system }

func TestOwnershipReadsNumericOwner(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid, gid, err := Ownership(info)
	if err != nil {
		t.Fatal(err)
	}
	if uid != uint32(os.Geteuid()) || gid != uint32(os.Getegid()) {
		t.Fatalf("owner = %d:%d, want %d:%d", uid, gid, os.Geteuid(), os.Getegid())
	}
}

func TestOwnershipRejectsUnsupportedMetadata(t *testing.T) {
	tests := []struct {
		name    string
		giveSys any
	}{
		{name: "missing"},
		{name: "different type", giveSys: struct{}{}},
		{name: "nil stat", giveSys: (*syscall.Stat_t)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := Ownership(ownershipInfo{system: tt.giveSys}); err == nil {
				t.Fatal("accepted unavailable ownership metadata")
			}
		})
	}
}
