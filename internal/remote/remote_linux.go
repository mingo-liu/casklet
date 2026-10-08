//go:build linux

// Package remote relays macOS client signals to a guest CLI session.
package remote

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

var sessionToken = regexp.MustCompile(`^[0-9a-f]{32}$`)

func socketPath(token string) (string, error) {
	if !sessionToken.MatchString(token) {
		return "", errors.New("invalid remote session token")
	}
	directory := "/run/casklet-remote"
	if os.Geteuid() != 0 {
		directory = fmt.Sprintf("/run/user/%d/casklet-remote", os.Geteuid())
	}
	if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("unsafe remote session directory")
	}
	return filepath.Join(directory, token+".sock"), nil
}

func validSignal(number int) bool {
	switch syscall.Signal(number) {
	case syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT:
		return true
	}
	return false
}

func Send(token, value string) error {
	number, err := strconv.Atoi(value)
	if err != nil || !validSignal(number) {
		return errors.New("invalid remote signal")
	}
	path, err := socketPath(token)
	if err != nil {
		return err
	}
	// A signal may arrive immediately after SSH connects, before the receiver
	// has bound its socket. Bound the retry rather than losing the interrupt.
	deadline := time.Now().Add(3 * time.Second)
	for {
		connection, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
		if err == nil {
			defer connection.Close()
			_, err = connection.Write([]byte(value))
			return err
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func Run(token string, args []string) (int, error) {
	path, err := socketPath(token)
	if err != nil {
		return 125, err
	}
	connection, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return 125, err
	}
	defer os.Remove(path)
	defer connection.Close()
	executable, err := os.Executable()
	if err != nil {
		return 125, err
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		return 125, err
	}
	control := make(chan syscall.Signal, 8)
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		var buffer [32]byte
		for {
			n, err := connection.Read(buffer[:])
			if err != nil {
				return
			}
			number, err := strconv.Atoi(string(buffer[:n]))
			if err == nil && validSignal(number) {
				select {
				case control <- syscall.Signal(number):
				case <-finished:
					return
				}
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case sig := <-signals:
			_ = cmd.Process.Signal(sig)
		case sig := <-control:
			_ = cmd.Process.Signal(sig)
		case err := <-done:
			if err == nil {
				return 0, nil
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				status := exit.Sys().(syscall.WaitStatus)
				if status.Signaled() {
					return 128 + int(status.Signal()), nil
				}
				return exit.ExitCode(), nil
			}
			return 125, err
		}
	}
}
