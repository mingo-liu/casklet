//go:build linux

package runtime

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Evaluate the policy on synthetic seccomp_data, including ABIs unavailable on
// the test host. This checks bypass boundaries without executing denied calls.
func TestSeccompPolicy(t *testing.T) {
	filters, err := seccompFilter()
	if err != nil {
		t.Fatal(err)
	}
	arch := uint32(unix.AUDIT_ARCH_AARCH64)
	if runtime.GOARCH == "amd64" {
		arch = unix.AUDIT_ARCH_X86_64
	}
	evaluate := func(nr, architecture, arg0, arg1 uint32) uint32 {
		t.Helper()
		data := map[uint32]uint32{0: nr, 4: architecture, 16: arg0, 24: arg1}
		var accumulator uint32
		for pc := 0; pc < len(filters); pc++ {
			f := filters[pc]
			switch f.Code {
			case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
				accumulator = data[f.K]
			case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K:
				if accumulator == f.K {
					pc += int(f.Jt)
				} else {
					pc += int(f.Jf)
				}
			case unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K:
				if accumulator&f.K != 0 {
					pc += int(f.Jt)
				} else {
					pc += int(f.Jf)
				}
			case unix.BPF_RET | unix.BPF_K:
				return f.K
			default:
				t.Fatalf("unsupported instruction %+v", f)
			}
		}
		t.Fatal("policy has no return")
		return 0
	}
	denied := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	for _, test := range []struct{ nr, arch, arg0, arg1, want uint32 }{
		{unix.SYS_READ, arch, 0, 0, unix.SECCOMP_RET_ALLOW},
		{unix.SYS_EXECVE, arch, 0, 0, unix.SECCOMP_RET_ALLOW},
		{unix.SYS_UNSHARE, arch, 0, 0, denied},
		{unix.SYS_SETNS, arch, 0, 0, denied},
		{unix.SYS_BPF, arch, 0, 0, denied},
		{unix.SYS_IO_URING_SETUP, arch, 0, 0, denied},
		{unix.SYS_CLONE, arch, unix.CLONE_VM | unix.CLONE_THREAD, 0, unix.SECCOMP_RET_ALLOW},
		{unix.SYS_CLONE, arch, unix.CLONE_NEWUSER, 0, denied},
		{unix.SYS_CLONE, arch, unix.CLONE_NEWPID, 0, denied},
		{unix.SYS_CLONE3, arch, 0, 0, unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{unix.SYS_IOCTL, arch, 0, unix.TIOCSTI, denied},
		{unix.SYS_IOCTL, arch, 0, unix.TIOCLINUX, denied},
		{unix.SYS_IOCTL, arch, 0, unix.TIOCGWINSZ, unix.SECCOMP_RET_ALLOW},
		{unix.SYS_READ, 0, 0, 0, unix.SECCOMP_RET_KILL_PROCESS},
	} {
		if got := evaluate(test.nr, test.arch, test.arg0, test.arg1); got != test.want {
			t.Fatalf("%+v: action %#x", test, got)
		}
	}
	if runtime.GOARCH == "amd64" && evaluate(0x40000000|unix.SYS_READ, arch, 0, 0) != unix.SECCOMP_RET_KILL_PROCESS {
		t.Fatal("x32 ABI bypass")
	}
}

func TestSeccompThreadSynchronization(t *testing.T) {
	if os.Getenv("CASKLET_TEST_SECCOMP") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSeccompThreadSynchronization$")
		cmd.Env = append(os.Environ(), "CASKLET_TEST_SECCOMP=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seccomp subprocess: %v: %s", err, out)
		}
		return
	}
	var ready, done sync.WaitGroup
	start := make(chan struct{})
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		ready.Add(1)
		done.Add(1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			defer done.Done()
			ready.Done()
			<-start
			if err := unix.Unshare(0); err != unix.EPERM {
				failures <- err
			}
		}()
	}
	ready.Wait()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := installSeccomp("default"); err != nil {
		t.Fatal(err)
	}
	close(start)
	done.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("existing thread bypassed filter: %v", err)
	}
}
