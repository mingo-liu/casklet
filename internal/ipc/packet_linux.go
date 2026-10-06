//go:build linux

// Package ipc exchanges bounded messages and close-on-exec file descriptors.
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

const MaxPacket = 64 << 10

func FD(file *os.File) int {
	connection, err := file.SyscallConn()
	if err != nil {
		return -1
	}
	fd := -1
	_ = connection.Control(func(value uintptr) { fd = int(value) })
	return fd
}

func CloseFiles(files []*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func Send(conn *net.UnixConn, value any, files []*os.File) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxPacket || len(files) > 8 {
		return errors.New("exec message exceeds protocol limits")
	}
	var rights []byte
	if len(files) > 0 {
		fds := make([]int, len(files))
		for i, file := range files {
			fds[i] = FD(file)
		}
		rights = unix.UnixRights(fds...)
	}
	n, _, err := conn.WriteMsgUnix(data, rights, nil)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

// Receive uses MSG_CMSG_CLOEXEC atomically, including while other goroutines fork.
func Receive(conn *net.UnixConn, maxFiles int) ([]byte, []*os.File, error) {
	connection, err := conn.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	var data []byte
	var files []*os.File
	var receiveErr error
	err = connection.Read(func(fd uintptr) bool {
		data, files, receiveErr = ReceiveFD(int(fd), maxFiles)
		return !errors.Is(receiveErr, unix.EAGAIN) && !errors.Is(receiveErr, unix.EWOULDBLOCK)
	})
	if err != nil {
		CloseFiles(files)
		return nil, nil, err
	}
	return data, files, receiveErr
}

func ReceiveFD(fd, maxFiles int) ([]byte, []*os.File, error) {
	if maxFiles < 0 || maxFiles > 8 {
		return nil, nil, errors.New("invalid descriptor limit")
	}
	data, ancillary := make([]byte, MaxPacket), make([]byte, unix.CmsgSpace(8*4)+unix.CmsgSpace(unix.SizeofUcred))
	n, oobn, flags, _, err := unix.Recvmsg(fd, data, ancillary, unix.MSG_CMSG_CLOEXEC)
	if err != nil {
		return nil, nil, err
	}
	messages, err := unix.ParseSocketControlMessage(ancillary[:oobn])
	if err != nil {
		return nil, nil, err
	}
	var files []*os.File
	var ancillaryErr error
	for _, message := range messages {
		if message.Header.Level != unix.SOL_SOCKET || message.Header.Type != unix.SCM_RIGHTS {
			ancillaryErr = errors.New("unexpected exec ancillary message")
			continue
		}
		fds, err := unix.ParseUnixRights(&message)
		if err != nil {
			ancillaryErr = errors.Join(ancillaryErr, err)
			continue
		}
		for _, fd := range fds {
			files = append(files, os.NewFile(uintptr(fd), "exec-descriptor"))
		}
	}
	// Recvmsg installs all received descriptors, even after an unexpected message.
	if ancillaryErr != nil {
		CloseFiles(files)
		return nil, nil, ancillaryErr
	}
	if n == 0 {
		CloseFiles(files)
		return nil, nil, io.EOF
	}
	if flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 || len(files) > maxFiles {
		CloseFiles(files)
		return nil, nil, errors.New("truncated or oversized exec message")
	}
	return data[:n], files, nil
}

func Decode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid exec message: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("exec message contains trailing data")
	}
	return nil
}

func Peer(conn *net.UnixConn) (*unix.Ucred, error) {
	connection, err := conn.SyscallConn()
	if err != nil {
		return nil, err
	}
	var credential *unix.Ucred
	var peerErr error
	err = connection.Control(func(fd uintptr) {
		credential, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	return credential, errors.Join(err, peerErr)
}
