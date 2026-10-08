package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/mingo-liu/casklet/internal/config"
)

const optionFileLimit = 1 << 20

var runBooleans = map[string]bool{"detach": true, "d": true, "interactive": true, "i": true, "tty": true, "t": true, "read-only": true, "rootless": true, "userns": true, "no-healthcheck": true}
var repeatedOptions = map[string]bool{"env": true, "env-file": true, "mount": true, "dns": true, "publish": true, "uid-map": true, "gid-map": true}
var runFileOptions = strings.Fields("rootfs image entrypoint detach name hostname memory pids-limit cpus timeout stop-timeout stop-signal restart log-max-size log-max-files seccomp userns rootless uid-map gid-map network dns publish interactive tty env env-file mount workdir user read-only progress health-cmd health-interval health-timeout health-retries health-start-period health-start-interval no-healthcheck")

func readOptionFile(filename string) ([]byte, error) {
	f, err := os.OpenFile(filename, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > optionFileLimit {
		return nil, errors.New("option file must be regular and at most 1 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, optionFileLimit+1))
	if len(data) > optionFileLimit {
		return nil, errors.New("option file exceeds 1 MiB")
	}
	return data, err
}

func readEnvironment(filename string) ([]string, error) {
	data, err := readOptionFile(filename)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), optionFileLimit+1)
	var env []string
	for line := 1; scanner.Scan(); line++ {
		value := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.TrimSpace(value) == "" || strings.HasPrefix(strings.TrimSpace(value), "#") {
			continue
		}
		if err := (config.Config{Command: []string{"true"}, Env: []string{value}}).ValidateExecution(); err != nil {
			return nil, fmt.Errorf("invalid environment assignment at line %d; use KEY=VALUE", line)
		}
		env = append(env, value)
	}
	return env, scanner.Err()
}

// optionTokens visits actual flags, skipping their values and the workload.
func optionTokens(args []string, visit func(name, value string, index, end int) error) error {
	for i := 1; i < len(args); i++ {
		if args[i] == "--" || !strings.HasPrefix(args[i], "-") {
			break
		}
		name, value, inline := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		end := i
		if !inline && !runBooleans[name] && name != "it" && name != "ti" && name != "help" && name != "h" {
			end++
			if end >= len(args) {
				return fmt.Errorf("missing value for --%s", name)
			}
			value = args[end]
		}
		if err := visit(name, value, i, end); err != nil {
			return err
		}
		i = end
	}
	return nil
}

func configFileArguments(filename string, explicit map[string]bool) ([]string, []string, error) {
	data, err := readOptionFile(filename)
	if err != nil {
		return nil, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	// Decode each key separately to reject duplicates as well as unknown options.
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, nil, errors.New("run config must be a JSON object")
	}
	values := map[string]json.RawMessage{}
	allowed := map[string]bool{"command": true}
	for _, name := range runFileOptions {
		allowed[name] = true
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		name := key.(string)
		if !allowed[name] || values[name] != nil {
			return nil, nil, fmt.Errorf("unknown or duplicate run config field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		values[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, nil, errors.New("unexpected data after run config")
	}
	var command []string
	if raw, ok := values["command"]; ok {
		if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &command) != nil || len(command) == 0 {
			return nil, nil, errors.New("config command must be a nonempty string array")
		}
		delete(values, "command")
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var args []string
	for _, name := range names {
		raw := values[name]
		if bytes.Equal(raw, []byte("null")) {
			return nil, nil, fmt.Errorf("config field %q cannot be null", name)
		}
		var entries []string
		switch {
		case repeatedOptions[name]:
			if json.Unmarshal(raw, &entries) != nil {
				return nil, nil, fmt.Errorf("config field %q must be a string array", name)
			}
		case runBooleans[name]:
			var value bool
			if json.Unmarshal(raw, &value) != nil {
				return nil, nil, fmt.Errorf("config field %q must be boolean", name)
			}
			entries = []string{fmt.Sprint(value)}
		default:
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return nil, nil, fmt.Errorf("config field %q must be a string", name)
			}
			entries = []string{value}
		}
		if explicit[name] && !repeatedOptions[name] {
			continue
		}
		if (name == "rootfs" || name == "image") && (explicit["rootfs"] || explicit["image"]) {
			continue
		}
		for _, value := range entries {
			if name == "rootfs" && value != "builtin:busybox" || name == "env-file" {
				if !filepath.IsAbs(value) {
					value = filepath.Join(filepath.Dir(filename), value)
				}
			}
			if name == "mount" {
				mount, err := config.ParseMount(value)
				if err != nil {
					return nil, nil, err
				}
				if mount.Type != "volume" && !filepath.IsAbs(mount.Source) {
					mount.Source = filepath.Join(filepath.Dir(filename), mount.Source)
					value = "type=bind,source=" + mount.Source + ",target=" + mount.Target
					if mount.ReadOnly {
						value += ",readonly"
					}
				}
			}
			args = append(args, "--"+name+"="+value)
		}
	}
	return args, command, nil
}

// expandFileArguments consumes host-local files before VM transport or sudo.
// Scalar CLI flags override config values; files precede all explicit env values.
func expandFileArguments(args []string) ([]string, error) {
	if len(args) == 0 || args[0] != "run" && args[0] != "exec" {
		return args, nil
	}
	explicit := map[string]bool{}
	configPath := ""
	configCount := 0
	help := false
	err := optionTokens(args, func(name, value string, index, end int) error {
		explicit[name] = true
		if name == "config" {
			configPath = value
			configCount++
		}
		help = help || name == "help" || name == "h"
		return nil
	})
	if err != nil {
		return nil, err
	}
	if help {
		return args, nil
	}
	if explicit["d"] {
		explicit["detach"] = true
	}
	if explicit["i"] || explicit["it"] || explicit["ti"] {
		explicit["interactive"] = true
	}
	if explicit["t"] || explicit["it"] || explicit["ti"] {
		explicit["tty"] = true
	}
	if explicit["p"] {
		explicit["publish"] = true
	}
	var fileArgs, command []string
	if configCount > 0 {
		if args[0] != "run" || configCount != 1 || configPath == "" {
			return nil, errors.New("run accepts exactly one nonempty --config path")
		}
		var err error
		fileArgs, command, err = configFileArguments(configPath, explicit)
		if err != nil {
			return nil, fmt.Errorf("read run config %s: %w", configPath, err)
		}
	}
	combined := append([]string{args[0]}, fileArgs...)
	err = optionTokens(args, func(name, value string, index, end int) error {
		if name != "config" {
			combined = append(combined, args[index:end+1]...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	consumed := 1
	_ = optionTokens(args, func(_, _ string, _, end int) error { consumed = end + 1; return nil })
	combined = append(combined, args[consumed:]...)
	hasCommand := false
	for _, arg := range args[consumed:] {
		hasCommand = hasCommand || arg == "--"
	}
	if !hasCommand && len(command) > 0 {
		combined = append(combined, "--")
		combined = append(combined, command...)
	}
	var envArgs []string
	result := []string{combined[0]}
	consumed = 1
	err = optionTokens(combined, func(name, value string, index, end int) error {
		consumed = end + 1
		if name == "env-file" {
			env, err := readEnvironment(value)
			if err != nil {
				return fmt.Errorf("read env file %s: %w", value, err)
			}
			for _, assignment := range env {
				envArgs = append(envArgs, "--env="+assignment)
			}
		} else {
			result = append(result, combined[index:end+1]...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result = append(append([]string{result[0]}, envArgs...), result[1:]...)
	result = append(result, combined[consumed:]...)
	return result, nil
}
