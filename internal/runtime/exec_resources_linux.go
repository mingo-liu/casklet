//go:build linux

package runtime

import (
	"errors"
	"fmt"
	"os"

	"github.com/mingo-liu/mini-docker/internal/ipc"
	"golang.org/x/sys/unix"
)

// Namespace handles are opened by init itself, before privileges are reduced.
// The supervisor never resolves a namespace from a persisted or reusable PID.
func sendExecNamespaces(socket int) error {
	var files []*os.File
	defer func() { ipc.CloseFiles(files) }()
	for _, path := range []string{"/proc/self/ns/mnt", "/proc/self/ns/uts", "/proc/self/ns/ipc", "/proc/self/ns/net", "/proc/self/ns/pid", "/"} {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		files = append(files, file)
	}
	fds := make([]int, len(files))
	for i, file := range files {
		fds[i] = ipc.FD(file)
	}
	return unix.Sendmsg(socket, []byte{'N'}, unix.UnixRights(fds...), nil, 0)
}

func receiveExecNamespaces(socket int) ([]*os.File, error) {
	data, files, err := ipc.ReceiveFD(socket, 6)
	if err != nil {
		return nil, err
	}
	if string(data) != "N" || len(files) != 6 {
		ipc.CloseFiles(files)
		return nil, errors.New("invalid container namespace bundle")
	}
	for i, kind := range []int{unix.CLONE_NEWNS, unix.CLONE_NEWUTS, unix.CLONE_NEWIPC, unix.CLONE_NEWNET, unix.CLONE_NEWPID} {
		actual, err := unix.IoctlRetInt(ipc.FD(files[i]), unix.NS_GET_NSTYPE)
		if err != nil || actual != kind {
			ipc.CloseFiles(files)
			return nil, fmt.Errorf("invalid namespace descriptor %d", i)
		}
	}
	info, err := files[5].Stat()
	if err != nil || !info.IsDir() {
		ipc.CloseFiles(files)
		return nil, errors.New("invalid container root descriptor")
	}
	return files, nil
}
