//go:build linux

package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type unitStatus struct {
	LoadState, ActiveState, Result string
	MainPID, ExitCode, ExitStatus  int
}

func unitName(id string) string { return "mini-docker-" + id + ".service" }

func (status unitStatus) live() bool {
	return status.MainPID != 0 || (status.ActiveState != "inactive" && status.ActiveState != "failed")
}

func checkSystemd() error {
	if os.Geteuid() != 0 {
		return errors.New("container management requires root; use scripts/run-linux.sh")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("background containers require a running systemd system instance")
	}
	for _, tool := range []string{"systemd-run", "systemctl"} {
		if _, err := exec.LookPath("/usr/bin/" + tool); err != nil {
			return fmt.Errorf("background containers require %s: %w", tool, err)
		}
	}
	return nil
}

func systemdCommand(ctx context.Context, tool string, args ...string) ([]byte, error) {
	if tool != "systemctl" && tool != "systemd-run" {
		return nil, errors.New("unknown systemd management tool")
	}
	// Resolve management tools independently of a caller-controlled PATH.
	cmd := exec.CommandContext(ctx, "/usr/bin/"+tool, args...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return output, ctx.Err()
		}
		return output, fmt.Errorf("%s: %w: %s", tool, err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func inspectUnit(ctx context.Context, id string) (unitStatus, error) {
	if err := validateID(id); err != nil {
		return unitStatus{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := systemdCommand(ctx, "systemctl", "show", "--property=LoadState,ActiveState,Result,ExecMainCode,ExecMainStatus,MainPID", unitName(id))
	if err != nil {
		return unitStatus{}, err
	}
	return parseUnitStatus(string(output))
}

func parseUnitStatus(output string) (unitStatus, error) {
	fields := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return unitStatus{}, errors.New("invalid systemd unit status")
		}
		if _, exists := fields[key]; exists {
			return unitStatus{}, errors.New("duplicate systemd unit status field")
		}
		fields[key] = value
	}
	status := unitStatus{LoadState: fields["LoadState"], ActiveState: fields["ActiveState"], Result: fields["Result"]}
	if status.LoadState == "" {
		return status, errors.New("systemd did not report the unit load state")
	}
	switch status.ActiveState {
	case "inactive", "failed", "active", "activating", "deactivating", "reloading", "maintenance":
	default:
		return status, errors.New("systemd reported an unknown unit activity state")
	}
	for key, target := range map[string]*int{"MainPID": &status.MainPID, "ExecMainCode": &status.ExitCode, "ExecMainStatus": &status.ExitStatus} {
		value, err := strconv.Atoi(fields[key])
		if err != nil || value < 0 {
			return status, fmt.Errorf("invalid systemd %s", key)
		}
		*target = value
	}
	if status.ExitStatus > 255 || status.ExitCode > 3 || (status.LoadState == "not-found" && status.live()) {
		return status, errors.New("inconsistent systemd unit status")
	}
	return status, nil
}

func stopUnit(ctx context.Context, id string) error {
	status, err := inspectUnit(ctx, id)
	if err != nil {
		return err
	}
	if status.LoadState == "not-found" {
		return nil
	}
	_, err = systemdCommand(ctx, "systemctl", "stop", unitName(id))
	return err
}
