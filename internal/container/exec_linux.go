//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/ipc"
	containerruntime "github.com/mingo-liu/mini-docker/internal/runtime"
	"golang.org/x/sys/unix"
)

const maxExecSessions = 16

type execMessage struct {
	Version int          `json:"version"`
	Kind    string       `json:"kind"`
	Options *config.Exec `json:"options,omitempty"`
	Signal  int          `json:"signal,omitempty"`
	Code    int          `json:"code,omitempty"`
	Error   string       `json:"error,omitempty"`
}

// Exec attaches caller-owned streams to an additional command in a live container.
// Signaling this invocation never targets the container's main process.
func Exec(ctx context.Context, ref string, options config.Exec, stdin, stdout, stderr *os.File, signals <-chan os.Signal) (code int, runErr error) {
	if err := options.Validate(); err != nil {
		return 125, err
	}
	if options.TTY && options.Interactive {
		if err := containerruntime.CheckExecTerminal(stdin); err != nil {
			return 125, err
		}
	}
	store, err := managementStore()
	if err != nil {
		return 125, err
	}
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	record, err := store.Get(startup, ref)
	if err == nil {
		record, err = refreshRecord(startup, store, record, false)
	}
	if err != nil {
		return 125, err
	}
	if record.State != StateRunning {
		return 125, errors.New("exec requires a running container")
	}
	status, err := inspectUnit(startup, record.ID, record.Generation)
	if err != nil {
		return 125, err
	}
	if !status.live() || status.MainPID <= 0 {
		return 125, errors.New("container supervisor is unavailable")
	}
	path := filepath.Join(store.root, record.ID, "exec.sock")
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return 125, fmt.Errorf("container exec endpoint unavailable: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != 0 || stat.Mode&0777 != 0600 {
		return 125, errors.New("unsafe container exec endpoint")
	}
	connection, err := (&net.Dialer{}).DialContext(startup, "unixpacket", path)
	if err != nil {
		return 125, fmt.Errorf("connect container exec: %w", err)
	}
	conn := connection.(*net.UnixConn)
	defer conn.Close()
	peer, err := ipc.Peer(conn)
	if err != nil || peer.Uid != 0 || int(peer.Pid) != status.MainPID {
		return 125, errors.New("container exec endpoint has an unexpected supervisor")
	}
	if !options.Interactive && !options.TTY {
		input, err := os.Open("/dev/null")
		if err != nil {
			return 125, err
		}
		defer input.Close()
		stdin = input
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return 125, err
	}
	if err := ipc.Send(conn, execMessage{Version: 1, Kind: "exec", Options: &options}, []*os.File{stdin, stdout, stderr}); err != nil {
		return 125, err
	}
	type packet struct {
		message execMessage
		files   []*os.File
		err     error
	}
	received := make(chan packet, 1)
	readerStop, readerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			data, files, err := ipc.Receive(conn, 1)
			var response execMessage
			if err == nil {
				err = ipc.Decode(data, &response)
			}
			select {
			case received <- packet{response, files, err}:
			case <-readerStop:
				ipc.CloseFiles(files)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	var terminal containerruntime.ExecTerminalIO
	var terminalErrors <-chan error
	defer func() {
		// Disconnect before draining the PTY so early failures cancel the job.
		close(readerStop)
		conn.Close()
		<-readerDone
		select {
		case packet := <-received:
			ipc.CloseFiles(packet.files)
		default:
		}
		if terminal != nil {
			runErr = errors.Join(runErr, terminal.Close())
		}
	}()
	done := ctx.Done()
	for {
		select {
		case packet := <-received:
			if packet.err != nil {
				ipc.CloseFiles(packet.files)
				return 125, fmt.Errorf("container exec interrupted: %w", packet.err)
			}
			response := packet.message
			if response.Kind == "terminal" && response.Version == 1 && response.Options == nil && response.Signal == 0 && response.Code == 0 && response.Error == "" && options.TTY && terminal == nil && len(packet.files) == 1 {
				terminal, err = containerruntime.StartExecTerminal(packet.files[0], stdin, stdout, options.Interactive)
				if err != nil {
					ipc.CloseFiles(packet.files)
					return 125, err
				}
				terminalErrors = terminal.Errors()
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if err := ipc.Send(conn, execMessage{Version: 1, Kind: "ready"}, nil); err != nil {
					return 125, err
				}
				continue
			}
			valid := response.Version == 1 && response.Kind == "result" && response.Code >= 0 && response.Code <= 255 && response.Options == nil && response.Signal == 0 && len(packet.files) == 0
			ipc.CloseFiles(packet.files)
			if !valid {
				return 125, errors.New("invalid container exec result")
			}
			if response.Error != "" {
				return response.Code, errors.New(response.Error)
			}
			return response.Code, nil
		case err := <-terminalErrors:
			return 125, fmt.Errorf("exec terminal: %w", err)
		case signal, ok := <-signals:
			if !ok {
				signals = nil
				continue
			}
			sig, ok := signal.(syscall.Signal)
			if !ok || !allowedExecSignal(sig) {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			if err := ipc.Send(conn, execMessage{Version: 1, Kind: "signal", Signal: int(sig)}, nil); err != nil {
				return 125, err
			}
		case <-done:
			done = nil
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = ipc.Send(conn, execMessage{Version: 1, Kind: "signal", Signal: int(syscall.SIGTERM)}, nil)
			_ = conn.SetReadDeadline(time.Now().Add(12 * time.Second))
		}
	}
}

func allowedExecSignal(signal syscall.Signal) bool {
	return signal == syscall.SIGINT || signal == syscall.SIGTERM || signal == syscall.SIGHUP || signal == syscall.SIGQUIT
}

type execServer struct {
	store     *Store
	id        string
	resources containerruntime.ExecResources
	ctx       context.Context
	cancel    context.CancelFunc
	listener  *net.UnixListener
	mu        sync.Mutex
	clients   map[*net.UnixConn]struct{}
	workers   sync.WaitGroup
	slots     chan struct{}
}

func newExecServer(store *Store, id string) *execServer {
	ctx, cancel := context.WithCancel(context.Background())
	return &execServer{store: store, id: id, ctx: ctx, cancel: cancel, clients: make(map[*net.UnixConn]struct{}), slots: make(chan struct{}, maxExecSessions)}
}

func (server *execServer) Start(resources containerruntime.ExecResources) error {
	path := filepath.Join(server.store.root, server.id, "exec.sock")
	// Existing endpoints belong to a previous attempt and are never replaced.
	if _, err := os.Lstat(path); err == nil {
		return errors.New("container exec endpoint already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: path, Net: "unixpacket"})
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		return err
	}
	server.listener, server.resources = listener, resources
	server.workers.Add(1)
	go server.accept()
	return nil
}

func (server *execServer) accept() {
	defer server.workers.Done()
	for {
		conn, err := server.listener.AcceptUnix()
		if err != nil {
			return
		}
		select {
		case server.slots <- struct{}{}:
		case <-server.ctx.Done():
			conn.Close()
			return
		default:
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = ipc.Send(conn, execMessage{Version: 1, Kind: "result", Code: 125, Error: "container exec session limit reached"}, nil)
			conn.Close()
			continue
		}
		server.mu.Lock()
		server.clients[conn] = struct{}{}
		server.mu.Unlock()
		server.workers.Add(1)
		go func() {
			defer server.workers.Done()
			defer func() { <-server.slots }()
			defer conn.Close()
			defer func() { server.mu.Lock(); delete(server.clients, conn); server.mu.Unlock() }()
			code, err := server.serve(conn)
			response := execMessage{Version: 1, Kind: "result", Code: code}
			if err != nil {
				response.Error = err.Error()
			}
			_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
			_ = ipc.Send(conn, response, nil)
		}()
	}
}

func (server *execServer) serve(conn *net.UnixConn) (int, error) {
	peer, err := ipc.Peer(conn)
	if err != nil || peer.Uid != 0 {
		return 125, errors.New("exec requires a root-owned client")
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, files, err := ipc.Receive(conn, 3)
	if err != nil {
		return 125, err
	}
	defer ipc.CloseFiles(files)
	var request execMessage
	if err := ipc.Decode(data, &request); err != nil {
		return 125, err
	}
	if request.Version != 1 || request.Kind != "exec" || request.Options == nil || request.Signal != 0 || request.Code != 0 || request.Error != "" || len(files) != 3 {
		return 125, errors.New("invalid exec request")
	}
	if err := request.Options.Validate(); err != nil {
		return 125, err
	}
	for _, file := range files {
		info, err := file.Stat()
		if err != nil || info.IsDir() || info.Mode()&os.ModeDevice != 0 && info.Mode()&os.ModeCharDevice == 0 {
			return 125, errors.New("invalid exec stream descriptor")
		}
	}
	lookup, cancelLookup := context.WithTimeout(server.ctx, 5*time.Second)
	record, err := server.store.Get(lookup, server.id)
	cancelLookup()
	if err != nil {
		return 125, err
	}
	if record.State != StateRunning {
		return 125, errors.New("exec requires a running container")
	}
	_ = conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(server.ctx)
	defer cancel()
	signals := make(chan syscall.Signal, 8)
	ready := make(chan struct{}, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer cancel()
		readyReceived := false
		for {
			data, files, err := ipc.Receive(conn, 0)
			ipc.CloseFiles(files)
			if err != nil {
				return
			}
			var signal execMessage
			if ipc.Decode(data, &signal) != nil || signal.Version != 1 || signal.Options != nil || signal.Code != 0 || signal.Error != "" {
				return
			}
			if signal.Kind == "ready" && signal.Signal == 0 && request.Options.TTY && !readyReceived {
				readyReceived = true
				ready <- struct{}{}
				continue
			}
			if signal.Kind != "signal" || !allowedExecSignal(syscall.Signal(signal.Signal)) {
				return
			}
			select {
			case signals <- syscall.Signal(signal.Signal):
			case <-ctx.Done():
				return
			}
		}
	}()
	var terminal containerruntime.ExecTerminal
	if request.Options.TTY {
		terminal = func(terminalCtx context.Context, master *os.File) error {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := ipc.Send(conn, execMessage{Version: 1, Kind: "terminal"}, []*os.File{master}); err != nil {
				return err
			}
			select {
			case <-ready:
				return nil
			case <-terminalCtx.Done():
				return terminalCtx.Err()
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	code, err := containerruntime.ExecuteInContainer(ctx, server.resources, *request.Options, files[0], files[1], files[2], signals, terminal)
	cancel()
	_ = conn.SetReadDeadline(time.Now())
	<-readerDone
	return code, err
}

func (server *execServer) Close() error {
	server.cancel()
	if server.listener != nil {
		_ = server.listener.Close()
	}
	server.mu.Lock()
	for client := range server.clients {
		_ = client.SetReadDeadline(time.Now())
	}
	server.mu.Unlock()
	server.workers.Wait()
	return nil
}
