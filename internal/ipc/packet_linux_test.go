//go:build linux

package ipc

import (
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func socketPair(t *testing.T) (int, int) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(pair[0]); unix.Close(pair[1]) })
	return pair[0], pair[1]
}

func unixConnection(t *testing.T, fd int) *net.UnixConn {
	t.Helper()
	duplicate, err := unix.Dup(fd)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(duplicate), "ipc-socket")
	conn, err := net.FileConn(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	result := conn.(*net.UnixConn)
	t.Cleanup(func() { result.Close() })
	return result
}

func TestPacketTransfersPipeWithCloseOnExec(t *testing.T) {
	left, right := socketPair(t)
	sender, receiver := unixConnection(t, left), unixConnection(t, right)
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer write.Close()
	want := struct {
		Command []string `json:"command"`
	}{[]string{"echo", "unchanged argument"}}
	if err := Send(sender, want, []*os.File{write}); err != nil {
		t.Fatal(err)
	}
	data, files, err := Receive(receiver, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer CloseFiles(files)
	if len(files) != 1 {
		t.Fatalf("received %d descriptors, want one", len(files))
	}
	flags, err := unix.FcntlInt(uintptr(FD(files[0])), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("received descriptor is not close-on-exec: flags=%d error=%v", flags, err)
	}
	var got struct {
		Command []string `json:"command"`
	}
	if err := Decode(data, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("received value = %+v, %v; want %+v", got, err, want)
	}
	write.Close()
	if _, err := files[0].Write([]byte("pipe output")); err != nil {
		t.Fatal(err)
	}
	files[0].Close()
	output, err := io.ReadAll(read)
	if err != nil || string(output) != "pipe output" {
		t.Fatalf("descriptor did not retain its pipe: %q, %v", output, err)
	}
}

func TestDecodeRejectsUnknownAndTrailingData(t *testing.T) {
	for _, input := range []string{
		`{"command":[],"unknown":true}`, `{"command":[]} {}`, `{"command":[]} false`,
		`{"command":[]} trailing`, `{"command":`, `{"command":123}`,
	} {
		var value struct {
			Command []string `json:"command"`
		}
		if err := Decode([]byte(input), &value); err == nil {
			t.Errorf("accepted invalid message %q", input)
		}
	}
	var value struct {
		Command []string `json:"command"`
	}
	if err := Decode([]byte("{\"command\": [\"echo\"]}\n\t "), &value); err != nil {
		t.Fatalf("rejected trailing whitespace: %v", err)
	}
}

func openDescriptorCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestReceiveRejectsOversizedPacketsWithoutDescriptorLeaks(t *testing.T) {
	left, right := socketPair(t)
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipeRead.Close()
	defer pipeWrite.Close()
	fd := FD(pipeWrite)
	baseline := openDescriptorCount(t)
	for _, test := range []struct {
		name     string
		data     []byte
		fds      []int
		maxFiles int
	}{
		{"excess descriptors", []byte("{}"), []int{fd, fd}, 1},
		{"unexpected descriptors", []byte("{}"), []int{fd}, 0},
		{"truncated data", []byte(strings.Repeat("x", MaxPacket+1)), []int{fd}, 1},
		{"truncated rights", []byte("{}"), []int{fd, fd, fd, fd, fd, fd, fd, fd, fd}, 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			for attempt := 0; attempt < 8; attempt++ {
				if err := unix.Sendmsg(left, test.data, unix.UnixRights(test.fds...), nil, 0); err != nil {
					t.Fatal(err)
				}
				data, files, err := ReceiveFD(right, test.maxFiles)
				CloseFiles(files)
				if err == nil || data != nil || len(files) != 0 {
					t.Fatalf("invalid packet accepted: bytes=%d descriptors=%d error=%v", len(data), len(files), err)
				}
				if count := openDescriptorCount(t); count != baseline {
					t.Fatalf("rejected packet leaked descriptors: before=%d after=%d", baseline, count)
				}
			}
		})
	}
}

func TestReceivePacketBoundariesAndEOF(t *testing.T) {
	left, right := socketPair(t)
	data := []byte(strings.Repeat("x", MaxPacket))
	if err := unix.Sendmsg(left, data, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	got, files, err := ReceiveFD(right, 0)
	CloseFiles(files)
	if err != nil || !reflect.DeepEqual(got, data) || len(files) != 0 {
		t.Fatalf("maximum-size packet failed: bytes=%d error=%v", len(got), err)
	}
	for _, limit := range []int{-1, 9} {
		if _, _, err := ReceiveFD(right, limit); err == nil {
			t.Errorf("accepted invalid descriptor limit %d", limit)
		}
	}
	if err := unix.Shutdown(left, unix.SHUT_WR); err != nil {
		t.Fatal(err)
	}
	if _, files, err := ReceiveFD(right, 0); !errors.Is(err, io.EOF) || len(files) != 0 {
		t.Fatalf("closed peer = descriptors:%d error:%v; want EOF", len(files), err)
	}
}

func TestReceiveUnexpectedAncillaryClosesDescriptors(t *testing.T) {
	left, right := socketPair(t)
	if err := unix.SetsockoptInt(right, unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	baseline := openDescriptorCount(t)
	if err := unix.Sendmsg(left, []byte("{}"), unix.UnixRights(FD(file)), nil, 0); err != nil {
		t.Fatal(err)
	}
	_, files, err := ReceiveFD(right, 1)
	CloseFiles(files)
	if err == nil || len(files) != 0 {
		t.Fatalf("accepted unexpected credential message: descriptors=%d error=%v", len(files), err)
	}
	if count := openDescriptorCount(t); count != baseline {
		t.Fatalf("unexpected ancillary message leaked descriptors: before=%d after=%d", baseline, count)
	}
}

func TestSendRejectsProtocolLimits(t *testing.T) {
	left, _ := socketPair(t)
	conn := unixConnection(t, left)
	if err := Send(conn, strings.Repeat("x", MaxPacket), nil); err == nil {
		t.Fatal("accepted JSON exceeding maximum packet size")
	}
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := Send(conn, struct{}{}, []*os.File{file, file, file, file, file, file, file, file, file}); err == nil {
		t.Fatal("accepted more than eight descriptors")
	}
	if err := Send(conn, make(chan int), nil); err == nil {
		t.Fatal("accepted non-JSON value")
	}
}

func TestPeerReturnsActualProcessCredentials(t *testing.T) {
	left, right := socketPair(t)
	for _, fd := range []int{left, right} {
		peer, err := Peer(unixConnection(t, fd))
		if err != nil {
			t.Fatal(err)
		}
		if int(peer.Pid) != os.Getpid() || int(peer.Uid) != os.Geteuid() || int(peer.Gid) != os.Getegid() {
			t.Fatalf("unexpected peer credentials: %+v", peer)
		}
	}
}
