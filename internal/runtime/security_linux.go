//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"syscall"
	"unsafe"

	"github.com/mingo-liu/mini-docker/internal/config"
	"golang.org/x/sys/unix"
)

// TSYNC installs one filter on every existing thread and on future descendants.
// Installing a per-thread prctl filter would leave Go's fork thread unrestricted.
func installSeccomp(profile string) error {
	if profile == "unconfined" {
		return nil
	}
	filters, err := seccompFilter()
	if err != nil {
		return err
	}
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	result, _, errno := unix.RawSyscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&program)))
	goruntime.KeepAlive(filters)
	if errno != 0 {
		return fmt.Errorf("install seccomp filter on all threads: %w", errno)
	}
	if result != 0 {
		return fmt.Errorf("seccomp synchronization failed for thread %d", result)
	}
	return nil
}

func seccompFilter() ([]unix.SockFilter, error) {
	var arch uint32
	switch goruntime.GOARCH {
	case "amd64":
		arch = unix.AUDIT_ARCH_X86_64
	case "arm64":
		arch = unix.AUDIT_ARCH_AARCH64
	default:
		return nil, errors.New("seccomp supports Linux arm64 and amd64 only")
	}
	load := func(offset uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: offset}
	}
	ret := func(value uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: value} }
	equal := func(value uint32, yes, no uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: value, Jt: yes, Jf: no}
	}
	denied := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	filters := []unix.SockFilter{load(4), equal(arch, 1, 0), ret(unix.SECCOMP_RET_KILL_PROCESS), load(0)}
	if goruntime.GOARCH == "amd64" {
		// x32 shares the x86-64 audit architecture but has another syscall ABI.
		filters = append(filters, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1}, ret(unix.SECCOMP_RET_KILL_PROCESS))
	}
	for _, number := range []uint32{
		unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_PTRACE,
		unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV,
		unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
		unix.SYS_USERFAULTFD, unix.SYS_IO_URING_SETUP,
		unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT,
		unix.SYS_MOVE_MOUNT, unix.SYS_OPEN_TREE, unix.SYS_FSOPEN,
		unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_FSPICK, unix.SYS_MOUNT_SETATTR,
		unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_OPEN_BY_HANDLE_AT,
		unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE,
		unix.SYS_KEXEC_LOAD, unix.SYS_KEXEC_FILE_LOAD, unix.SYS_REBOOT,
		unix.SYS_SWAPON, unix.SYS_SWAPOFF, unix.SYS_QUOTACTL,
	} {
		filters = append(filters, equal(number, 0, 1), ret(denied))
	}
	// clone3 arguments are behind a pointer; ENOSYS permits libc's clone fallback.
	filters = append(filters, equal(unix.SYS_CLONE3, 0, 1), ret(unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS)))
	mask := uint32(namespaceFlags | unix.CLONE_NEWUSER | unix.CLONE_NEWCGROUP | unix.CLONE_PTRACE)
	filters = append(filters, equal(unix.SYS_CLONE, 0, 3), load(16), unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: mask, Jf: 1}, ret(denied), load(0))
	filters = append(filters, equal(unix.SYS_IOCTL, 0, 4), load(24), equal(unix.TIOCSTI, 1, 0), equal(unix.TIOCLINUX, 0, 1), ret(denied), ret(unix.SECCOMP_RET_ALLOW))
	return filters, nil
}

func namespaceAttributes(cfg config.Config) *syscall.SysProcAttr {
	attributes := &syscall.SysProcAttr{Cloneflags: namespaceFlags, Setpgid: true}
	if cfg.UserNS {
		attributes.Cloneflags |= unix.CLONE_NEWUSER
		attributes.GidMappingsEnableSetgroups = !cfg.Rootless
		attributes.Credential = &syscall.Credential{Uid: 0, Gid: 0, NoSetGroups: cfg.Rootless}
		for _, m := range cfg.UIDMappings {
			attributes.UidMappings = append(attributes.UidMappings, syscall.SysProcIDMap{ContainerID: int(m.ContainerID), HostID: int(m.HostID), Size: int(m.Size)})
		}
		for _, m := range cfg.GIDMappings {
			attributes.GidMappings = append(attributes.GidMappings, syscall.SysProcIDMap{ContainerID: int(m.ContainerID), HostID: int(m.HostID), Size: int(m.Size)})
		}
	}
	return attributes
}

func validateExecutionMode(cfg config.Config) error {
	if cfg.Rootless {
		if os.Geteuid() == 0 {
			return errors.New("--rootless requires an unprivileged host user")
		}
		if cfg.UIDMappings[0].HostID != uint32(os.Geteuid()) || cfg.GIDMappings[0].HostID != uint32(os.Getegid()) {
			return errors.New("rootless mappings must use the caller's effective UID and GID")
		}
	} else if os.Geteuid() != 0 {
		return errors.New("container execution requires root unless --rootless is selected")
	}
	return nil
}

// Copies are private and contain no special files. Never shift bind sources or
// follow symlinks; copied template ownership is uniformly container root.
func mapRootOwnership(ctxCheck func() error, path string, cfg config.Config) error {
	if !cfg.UserNS || cfg.Rootless {
		return nil
	}
	uid, _ := config.MappedID(0, cfg.UIDMappings)
	gid, _ := config.MappedID(0, cfg.GIDMappings)
	return filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctxCheck(); err != nil {
			return err
		}
		return os.Lchown(path, int(uid), int(gid))
	})
}
