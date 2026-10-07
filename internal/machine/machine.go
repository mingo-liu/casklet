package machine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

//go:embed assets/*
var assets embed.FS

type Machine struct {
	lima      string
	directory string
	stderr    io.Writer
}

func open(stderr io.Writer) (*Machine, error) {
	if os.Geteuid() == 0 {
		return nil, errors.New("run mdocker as your regular macOS user, without sudo")
	}
	lima, err := exec.LookPath("limactl")
	if err != nil {
		return nil, errors.New("Lima is required; install it with brew install lima")
	}
	directory, err := stateDirectory()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	return &Machine{lima: lima, directory: directory, stderr: stderr}, nil
}

func (m *Machine) command(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, m.lima, append([]string{"--tty=false"}, args...)...)
	cmd.Env = append(os.Environ(), "LIMA_SSH_PORT_FORWARDER=false")
	var output, diagnostic strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &diagnostic
	if args[0] == "start" || args[0] == "stop" {
		cmd.Stderr = io.MultiWriter(&diagnostic, m.stderr)
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Lima %s: %w: %s", args[0], err, strings.TrimSpace(diagnostic.String()))
	}
	return []byte(output.String()), nil
}

func (m *Machine) instance(ctx context.Context) (*Instance, error) {
	data, err := m.command(ctx, "list", "--json")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var instance Instance
		if err := decoder.Decode(&instance); errors.Is(err, io.EOF) {
			return nil, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode Lima instance: %w", err)
		}
		if instance.Name != Name {
			continue
		}
		if instance.Config.Plain {
			return nil, errors.New("the runtime machine must have file sharing and the guest agent enabled")
		}
		return &instance, nil
	}
}

func (m *Machine) lock(ctx context.Context) (*os.File, error) {
	fd, err := unix.Open(filepath.Join(m.directory, "machine.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "machine.lock")
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (m *Machine) ensure(ctx context.Context, options Options, initialize bool) (*Instance, error) {
	lock, err := m.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	instance, err := m.instance(ctx)
	if err != nil {
		return nil, err
	}
	if initialize && instance != nil {
		return nil, errors.New("runtime machine already exists; initialization options apply only when creating it")
	}
	if instance == nil {
		// Check the payload before creating a VM that cannot execute workloads.
		if _, err := enginePayload(); err != nil {
			return nil, err
		}
		data, err := configData(options)
		if err != nil {
			return nil, err
		}
		config := filepath.Join(m.directory, "machine.yaml")
		if err := os.WriteFile(config, data, 0600); err != nil {
			return nil, err
		}
		fmt.Fprintln(m.stderr, "Creating the mini-docker runtime machine...")
		if _, err := m.command(ctx, "start", "--name", Name, "--timeout", "10m", config); err != nil {
			return nil, err
		}
	} else if instance.Status != "Running" {
		fmt.Fprintln(m.stderr, "Starting the mini-docker runtime machine...")
		if _, err := m.command(ctx, "start", "--timeout", "10m", Name); err != nil {
			return nil, err
		}
	}
	instance, err = m.instance(ctx)
	if err != nil {
		return nil, err
	}
	if instance == nil || instance.Status != "Running" {
		return nil, errors.New("runtime machine did not become ready")
	}
	if instance.Arch != map[string]string{"arm64": "aarch64", "amd64": "x86_64"}[runtime.GOARCH] {
		return nil, errors.New("runtime machine architecture does not match the macOS client")
	}
	if err := m.install(ctx, *instance); err != nil {
		return nil, err
	}
	return instance, nil
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
func remoteCommand(args []string) string {
	parts := make([]string, len(args))
	for i, value := range args {
		parts[i] = quote(value)
	}
	return "exec " + strings.Join(parts, " ")
}

func sshCommand(ctx context.Context, instance Instance, tty bool, args ...string) *exec.Cmd {
	terminal := "-T"
	if tty {
		terminal = "-tt"
	}
	return exec.CommandContext(ctx, "/usr/bin/ssh", "-F", instance.SSHConfigFile, "-o", "BatchMode=yes", "-o", "LogLevel=ERROR", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", terminal, "--", "lima-"+instance.Name, remoteCommand(args))
}

func (m *Machine) output(ctx context.Context, instance Instance, args ...string) ([]byte, error) {
	cmd := sshCommand(ctx, instance, false, args...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("guest operation: %w: %s", err, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func enginePayload() ([]byte, error) {
	payload, err := assets.ReadFile("assets/mdocker-engine")
	if err != nil {
		return nil, errors.New("this client has no bundled engine; build it with make build")
	}
	binary, err := elf.NewFile(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("invalid bundled Linux engine: %w", err)
	}
	defer binary.Close()
	if binary.Machine != map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64}[runtime.GOARCH] {
		return nil, errors.New("bundled engine architecture does not match the client; rebuild with make build")
	}
	return payload, nil
}

// A matching marker is usable only while both installed executables remain
// available. Check them in one SSH request before accepting the cached payload.
const installationCheckScript = `test -f "$1" && test -x "$1" && test -f "$2/bin/busybox" && test -x "$2/bin/busybox" && test "$(cat "$3")" = "$4"`

func (m *Machine) install(ctx context.Context, instance Instance) error {
	payload, err := enginePayload()
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(payload))
	if _, err := m.output(ctx, instance, "/bin/sh", "-c", installationCheckScript, "check-install", guestEngine, guestRootFS, "/usr/local/lib/mini-docker/engine.sha256", hash); err == nil {
		return nil
	}
	fmt.Fprintln(m.stderr, "Installing the bundled container engine...")
	stage, err := os.MkdirTemp(m.directory, "install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	guestStage, err := instance.hostPath(stage)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "mdocker"), payload, 0700); err != nil {
		return err
	}
	prepare, err := assets.ReadFile("assets/prepare-rootfs.sh")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "prepare-rootfs.sh"), prepare, 0600); err != nil {
		return err
	}
	// Atomic executable replacement lets existing supervisors finish with their
	// pinned binary. Persistent container/image storage is never replaced.
	script := `set -eu
install -d -m 0755 /usr/local/lib/mini-docker
install -m 0755 "$1/mdocker" /usr/local/bin/mdocker.new
mv /usr/local/bin/mdocker.new /usr/local/bin/mdocker
if [ ! -e /var/lib/mini-docker/templates/busybox ]; then
  /bin/sh "$1/prepare-rootfs.sh" /var/lib/mini-docker/templates/busybox
fi
printf '%s\n' "$2" > /usr/local/lib/mini-docker/engine.sha256
`
	_, err = m.output(ctx, instance, "sudo", "-n", "--", "/bin/sh", "-c", script, "install", guestStage, hash)
	return err
}

func parseHostCommand(args []string) (Options, error) {
	options := defaultOptions()
	if handled, err := directHostHelp(args); handled {
		return options, err
	}
	if len(args) == 0 {
		return options, errors.New("a host command is required")
	}
	if args[0] == "rootfs" {
		if len(args) != 2 || args[1] == "" {
			return options, errors.New("rootfs requires one destination directory")
		}
		return options, nil
	}
	if args[0] != "machine" {
		return options, fmt.Errorf("unknown host command %q", args[0])
	}
	if len(args) < 2 {
		return options, errors.New("machine requires init, start, stop, status, or share")
	}
	switch args[1] {
	case "init":
		fs := flag.NewFlagSet("machine init", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.IntVar(&options.CPUs, "cpus", options.CPUs, "VM CPU count")
		fs.IntVar(&options.Memory, "memory", options.Memory, "VM memory in GiB")
		fs.IntVar(&options.Disk, "disk", options.Disk, "VM disk in GiB")
		fs.Func("mount", "additional shared directory", func(value string) error { options.Mounts = append(options.Mounts, value); return nil })
		if err := fs.Parse(args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return options, initHelpResult(args[2:])
			}
			return options, err
		}
		if fs.NArg() != 0 {
			return options, errors.New("unexpected machine init argument")
		}
		_, err := configData(options)
		return options, err
	case "share":
		if len(args) != 3 || args[2] == "" {
			return options, errors.New("machine share requires one existing directory")
		}
		path, err := sharedDirectory(args[2])
		options.Mounts = []string{path}
		return options, err
	case "start", "stop", "status":
		if len(args) != 2 {
			return options, fmt.Errorf("machine %s takes no arguments", args[1])
		}
		return options, nil
	default:
		return options, fmt.Errorf("unknown machine command %q", args[1])
	}
}

func HostCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	options, err := parseHostCommand(args)
	if errors.Is(err, flag.ErrHelp) {
		text, helpErr := Help(hostCommandTopic(args))
		if helpErr != nil {
			return helpErr
		}
		_, err = fmt.Fprint(stdout, text)
		return err
	}
	if err != nil {
		return fmt.Errorf("%w\nHint: run mdocker %s --help for usage and examples.", err, hostCommandTopic(args))
	}
	var parent string
	if args[0] == "rootfs" {
		parent, err = exportParent(args[1])
		if err != nil {
			return err
		}
	}
	m, err := open(stderr)
	if err != nil {
		return err
	}
	if args[0] == "rootfs" {
		// Validate the share using the parent, not the new template destination.
		if err := m.preflightShares(ctx, []string{"run", "--rootfs", parent}); err != nil {
			return err
		}
		instance, err := m.ensure(ctx, options, false)
		if err != nil {
			return err
		}
		return m.rootfs(ctx, *instance, args[1], stdout)
	}
	switch args[1] {
	case "share":
		return m.share(ctx, options.Mounts[0], stdout)
	case "init":
		_, err = m.ensure(ctx, options, true)
	case "start":
		_, err = m.ensure(ctx, options, false)
	case "stop":
		lock, lockErr := m.lock(ctx)
		if lockErr != nil {
			return lockErr
		}
		defer lock.Close()
		instance, instanceErr := m.instance(ctx)
		if instanceErr != nil {
			return instanceErr
		}
		if instance != nil && instance.Status == "Running" {
			_, err = m.command(ctx, "stop", Name)
		}
	case "status":
		instance, instanceErr := m.instance(ctx)
		if instanceErr != nil {
			return instanceErr
		}
		if instance == nil {
			_, err = fmt.Fprintln(stdout, Name+"\tnot initialized")
		} else {
			_, err = fmt.Fprintf(stdout, "%s\t%s\t%s\n", instance.Name, instance.Status, instance.Arch)
		}
		return err
	default:
		return fmt.Errorf("unknown machine command %q", args[1])
	}
	if err == nil {
		_, err = fmt.Fprintln(stdout, Name)
	}
	return err
}

func (m *Machine) rootfs(ctx context.Context, instance Instance, destination string, stdout io.Writer) error {
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rootfs destination already exists or is unavailable: %s", absolute)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(absolute), ".mdocker-rootfs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	guestStage, err := instance.hostPath(stage)
	if err != nil {
		return err
	}
	if _, err := m.output(ctx, instance, "sudo", "-n", "--", "cp", "-R", "--no-preserve=ownership", guestRootFS+"/.", guestStage); err != nil {
		return err
	}
	if err := os.Chmod(stage, 0755); err != nil {
		return err
	}
	if err := os.Rename(stage, absolute); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "Prepared "+absolute)
	return err
}
