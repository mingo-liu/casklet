package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Probe across Go threads, including threads created after filter installation.
func securityProbe(filtered bool) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "CapInh:", "CapPrm:", "CapEff:", "CapBnd:", "CapAmb:":
			if strings.Trim(fields[1], "0") != "" {
				fmt.Fprintln(os.Stderr, "capabilities were retained:", line)
				os.Exit(1)
			}
		case "NoNewPrivs:":
			if fields[1] != "1" {
				fmt.Fprintln(os.Stderr, "no_new_privs is missing")
				os.Exit(1)
			}
		case "Seccomp:":
			if filtered && fields[1] != "2" {
				fmt.Fprintln(os.Stderr, "seccomp filtering is missing")
				os.Exit(1)
			}
		}
	}

	var workers sync.WaitGroup
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			err := unix.Unshare(0)
			if filtered && err != unix.EPERM || !filtered && err != nil {
				failures <- fmt.Errorf("unshare(0): %v", err)
				return
			}
			_, _, errno := unix.Syscall(unix.SYS_IOCTL, ^uintptr(0), unix.TIOCSTI, 0)
			expected := unix.EBADF
			if filtered {
				expected = unix.EPERM
			}
			if errno != expected {
				failures <- fmt.Errorf("TIOCSTI: %v, want %v", errno, expected)
			}
		}()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("security-ok")
}

// Match setpriv-based entrypoints: drop the bounding set before switching IDs.
func imagePrivilegeDrop() {
	fail := func(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
	data, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		fail(err)
	}
	last, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fail(err)
	}
	for capability := 0; capability <= last; capability++ {
		_, _, errno := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, unix.PR_CAPBSET_DROP, uintptr(capability), 0, 0, 0, 0)
		if errno != 0 {
			fail(fmt.Errorf("drop bounding capability %d: %w", capability, errno))
		}
	}
	if err := syscall.Setgroups(nil); err != nil {
		fail(err)
	}
	if err := syscall.Setresgid(456, 456, 456); err != nil {
		fail(err)
	}
	if err := syscall.Setresuid(123, 123, 123); err != nil {
		fail(err)
	}
	securityProbe(true)
	fmt.Println("image-drop-ok")
}
