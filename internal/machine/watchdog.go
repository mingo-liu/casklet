package machine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// A separate process owns the read end of the client's lifeline. Unlike an
// in-process goroutine, it can notify the guest after the client receives SIGKILL.
func startWatchdog(instance Instance, token string, rootless bool) (func(), error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		read.Close()
		write.Close()
		return nil, err
	}
	data, err := json.Marshal(instance)
	if err != nil {
		read.Close()
		write.Close()
		return nil, err
	}
	cmd := exec.Command(executable, "__watchdog", string(data), token, strconv.FormatBool(rootless))
	cmd.ExtraFiles = []*os.File{read}
	if err := cmd.Start(); err != nil {
		read.Close()
		write.Close()
		return nil, err
	}
	read.Close()
	return func() {
		// A marker distinguishes a completed command from abrupt parent death.
		_, _ = write.Write([]byte{1})
		write.Close()
		_ = cmd.Wait()
	}, nil
}

// Watchdog is a private macOS entry point. FD 3 is its only parent lifeline.
func Watchdog(encoded, token, rootlessValue string) error {
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	var instance Instance
	if err := json.Unmarshal([]byte(encoded), &instance); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 16 || instance.Name != Name || instance.SSHConfigFile == "" {
		return errors.New("invalid watchdog session")
	}
	rootless, err := strconv.ParseBool(rootlessValue)
	if err != nil {
		return err
	}
	lifeline := os.NewFile(3, "client-lifeline")
	defer lifeline.Close()
	var marker [1]byte
	n, err := lifeline.Read(marker[:])
	if n > 0 {
		return nil
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := &Machine{}
	_, err = m.output(ctx, instance, engineCommand(rootless, "__signal", token, strconv.Itoa(int(syscall.SIGHUP)))...)
	return err
}
