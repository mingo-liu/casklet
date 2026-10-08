//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// EnsureRestartManager installs the boot-enabled guest service. Existing workload
// supervisors are independent and keep running during a manager upgrade.
func EnsureRestartManager(ctx context.Context, replace bool) error {
	if err := checkSystemd(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if strings.ContainsAny(exe, "\n\r\x00") {
		return errors.New("invalid restart manager executable path")
	}
	path := "/etc/systemd/system/" + restartUnit
	body := []byte("[Unit]\nDescription=casklet automatic container restarts\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=exec\nExecStart=" + strconv.Quote(strings.ReplaceAll(exe, "%", "%%")) + " __restart-manager\nRestart=on-failure\nRestartSec=2s\nTimeoutStopSec=45s\n\n[Install]\nWantedBy=multi-user.target\n")
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err == nil {
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != 0 || st.Mode&0022 != 0 || st.Nlink != 1 {
			return errors.New("unsafe restart manager unit file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	prior, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	changed := string(prior) != string(body)
	if changed {
		if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("unsafe restart manager unit file")
		}
		stage, err := os.CreateTemp(filepath.Dir(path), ".casklet-restarts-")
		if err != nil {
			return err
		}
		defer os.Remove(stage.Name())
		if _, err = stage.Write(body); err == nil {
			err = stage.Chmod(0644)
		}
		if err == nil {
			err = stage.Sync()
		}
		closeErr := stage.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(stage.Name(), path); err != nil {
			return err
		}
		if _, err := systemdCommand(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := systemdCommand(ctx, "systemctl", "enable", restartUnit); err != nil {
		return err
	}
	action := "start"
	if changed || replace {
		action = "restart"
	}
	if _, err := systemdCommand(ctx, "systemctl", action, restartUnit); err != nil {
		return fmt.Errorf("ensure restart manager: %w", err)
	}
	return nil
}
