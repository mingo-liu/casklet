package image

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mingo-liu/casklet/internal/config"
)

// LaunchConfig contains the execution defaults covered by an OCI image identity.
type LaunchConfig struct {
	StopSignal string   `json:"stop_signal,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
	Env        []string `json:"env,omitempty"`
	Workdir    string   `json:"workdir,omitempty"`
	User       string   `json:"user,omitempty"`
}

func validateLaunch(defaults *LaunchConfig) error {
	cfg := config.Config{Command: append(append([]string(nil), defaults.Entrypoint...), defaults.Cmd...), Env: defaults.Env, Workdir: defaults.Workdir, StopSignal: defaults.StopSignal}
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"/no-image-default"}
	}
	if strings.ContainsRune(defaults.User, 0) {
		return errors.New("image user cannot contain NUL")
	}
	return cfg.ValidateExecution()
}

// Apply merges defaults and resolves image account names inside the pinned tree.
// User arguments replace Cmd and are appended to Entrypoint. An explicit
// entrypoint replaces the image entrypoint and clears its default Cmd.
func (defaults LaunchConfig) Apply(cfg config.Config, tree string, entrypoint *string) (config.Config, error) {
	if cfg.StopSignal == "" {
		cfg.StopSignal = defaults.StopSignal
	}
	args := cfg.Command
	entry := defaults.Entrypoint
	if entrypoint != nil {
		entry = nil
		if *entrypoint != "" {
			entry = []string{*entrypoint}
		}
	} else if len(args) == 0 {
		args = defaults.Cmd
	}
	cfg.Command = append(append([]string(nil), entry...), args...)
	cfg.Env = append(append([]string(nil), defaults.Env...), cfg.Env...)
	if cfg.Workdir == "" {
		cfg.Workdir = defaults.Workdir
	}
	if cfg.User == nil && defaults.User != "" {
		var err error
		cfg.User, err = resolveUser(tree, defaults.User)
		if err != nil {
			return cfg, fmt.Errorf("resolve image user: %w", err)
		}
	}
	return cfg, cfg.ValidateExecution()
}

func numericID(value string) (uint32, error) {
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil || id == uint64(^uint32(0)) {
		return 0, errors.New("invalid numeric account ID")
	}
	return uint32(id), nil
}

func accountRows(root *os.Root, filename string) ([][]string, error) {
	name, err := resolvePath(root, filename, true)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("image account file must be a regular file no larger than 1 MiB")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, (1<<20)+1))
	var rows [][]string
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" && !strings.HasPrefix(line, "#") {
			rows = append(rows, strings.Split(line, ":"))
		}
	}
	return rows, scanner.Err()
}

func resolveUser(tree, value string) (*config.User, error) {
	root, err := os.OpenRoot(tree)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	user, group, hasGroup := strings.Cut(value, ":")
	if user == "" || (hasGroup && group == "") || strings.Contains(group, ":") {
		return nil, errors.New("image user must be USER[:GROUP]")
	}
	uid, uidErr := numericID(user)
	identity := &config.User{UID: uid}
	rows, err := accountRows(root, "etc/passwd")
	if err != nil {
		return nil, err
	}
	found := false
	for _, row := range rows {
		if len(row) < 7 {
			continue
		}
		rowUID, err := numericID(row[2])
		if err != nil {
			continue
		}
		if (uidErr == nil && rowUID == uid) || (uidErr != nil && row[0] == user) {
			identity.UID = rowUID
			identity.GID, err = numericID(row[3])
			if err != nil {
				return nil, err
			}
			found = true
			break
		}
	}
	if uidErr != nil && !found {
		return nil, fmt.Errorf("user %q is absent from /etc/passwd", user)
	}
	if hasGroup {
		gid, err := numericID(group)
		if err == nil {
			identity.GID = gid
			return identity, nil
		}
		rows, err = accountRows(root, "etc/group")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if len(row) >= 3 && row[0] == group {
				identity.GID, err = numericID(row[2])
				return identity, err
			}
		}
		return nil, fmt.Errorf("group %q is absent from /etc/group", group)
	}
	return identity, nil
}
