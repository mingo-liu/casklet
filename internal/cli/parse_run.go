package cli

import (
	"errors"
	"flag"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/mingo-liu/casklet/internal/config"
	"github.com/mingo-liu/casklet/internal/container"
	"github.com/mingo-liu/casklet/internal/image"
)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func parseRun(r Request, args []string) (Request, error) {
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if r.Action == "run" {
		fs.String("env-file", "", "environment assignments file")
		fs.String("config", "", "JSON run configuration")
	}
	fs.StringVar(&r.Config.RootFS, "rootfs", "", "rootfs template")
	var memory, cpus, user string
	if r.Action == "run" {
		fs.StringVar(&r.Config.Seccomp, "seccomp", "default", "seccomp profile")
		fs.BoolVar(&r.Config.UserNS, "userns", false, "user namespace")
		fs.BoolVar(&r.Config.Rootless, "rootless", false, "rootless foreground execution")
		fs.Func("uid-map", "UID mapping", func(value string) error {
			m, err := config.ParseIDMapping(value)
			if err == nil {
				r.Config.UIDMappings = append(r.Config.UIDMappings, m)
			}
			return err
		})
		fs.Func("gid-map", "GID mapping", func(value string) error {
			m, err := config.ParseIDMapping(value)
			if err == nil {
				r.Config.GIDMappings = append(r.Config.GIDMappings, m)
			}
			return err
		})
		fs.Func("log-max-size", "maximum log file size", func(value string) error {
			size, err := ParseMemory(value)
			r.Config.LogMaxSize = size
			return err
		})
		fs.IntVar(&r.Config.LogMaxFiles, "log-max-files", 4, "retained log files")
		fs.StringVar(&r.Progress, "progress", "auto", "image progress mode: auto, plain, or tty")
		fs.StringVar(&r.Config.Image, "image", "", "image reference or local image ID")
		fs.Func("entrypoint", "replace the image entrypoint", func(value string) error { r.Entrypoint = &value; return nil })
		fs.StringVar(&r.Config.Network, "network", "none", "network mode")
		fs.Func("dns", "IPv4 DNS server", func(value string) error { r.Config.DNS = append(r.Config.DNS, value); return nil })
		publish := func(value string) error {
			mapping, err := config.ParsePortMapping(value)
			if err == nil {
				r.Config.Publish = append(r.Config.Publish, mapping)
			}
			return err
		}
		fs.Func("publish", "published port", publish)
		fs.Func("p", "published port", publish)
		fs.BoolVar(&r.Detach, "detach", false, "run in the background")
		fs.BoolVar(&r.Detach, "d", false, "run in the background")
		fs.BoolVar(&r.Config.Interactive, "interactive", false, "forward stdin")
		fs.BoolVar(&r.Config.Interactive, "i", false, "forward stdin")
		fs.BoolVar(&r.Config.TTY, "tty", false, "allocate a terminal")
		fs.BoolVar(&r.Config.TTY, "t", false, "allocate a terminal")
		fs.StringVar(&r.Name, "name", "", "detached container name")
		fs.StringVar(&r.Config.Hostname, "hostname", "casklet", "hostname")
		fs.StringVar(&memory, "memory", "128m", "memory limit")
		fs.Int64Var(&r.Config.PidsLimit, "pids-limit", 64, "process and thread limit")
		fs.Func("restart", "detached restart policy", func(value string) error {
			if value == "" {
				return errors.New("--restart requires a policy")
			}
			if _, _, err := config.ParseRestartPolicy(value); err != nil {
				return err
			}
			r.Config.RestartPolicy = value
			return nil
		})
		fs.Func("stop-signal", "Linux signal for managed shutdown", func(value string) error {
			if _, err := config.ParseStopSignal(value); err != nil {
				return err
			}
			r.Config.StopSignal = value
			return nil
		})
		fs.Func("stop-timeout", "graceful shutdown duration", func(value string) error {
			duration, err := time.ParseDuration(value)
			if err != nil {
				return err
			}
			if err := config.ValidateStopTimeout(duration); err != nil {
				return err
			}
			r.Config.StopTimeout = &duration
			return nil
		})
		fs.DurationVar(&r.Config.Timeout, "timeout", 0, "command timeout")
		fs.StringVar(&cpus, "cpus", "0", "CPU cores")
		fs.Func("env", "environment assignment", func(value string) error {
			r.Config.Env = append(r.Config.Env, value)
			return nil
		})
		fs.Func("mount", "directory bind mount", func(value string) error {
			mount, err := config.ParseMount(value)
			if err != nil {
				return err
			}
			r.Config.Mounts = append(r.Config.Mounts, mount)
			return nil
		})
		fs.StringVar(&r.Config.Workdir, "workdir", "", "working directory")
		fs.StringVar(&user, "user", "", "numeric user and group IDs")
		fs.BoolVar(&r.Config.ReadOnly, "read-only", false, "read-only root filesystem")
	}
	options := args
	separator := -1
	for i, arg := range options {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator >= 0 {
		r.Config.Command = append([]string(nil), options[separator+1:]...)
		options = options[:separator]
	}
	if r.Action == "run" {
		options = expandTerminalFlags(fs, options)
	}
	if err := fs.Parse(options); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parseFlagHelp(r.Action, fs, args)
		}
		return r, err
	}
	if fs.NArg() != 0 {
		return r, errors.New("unexpected positional argument before --; place -- before the container command (example: -- /bin/echo hello)")
	}
	if r.Action == "doctor" && r.Config.RootFS == "" {
		return r, errors.New("--rootfs is required")
	}
	if r.Action == "run" {
		if err := validateProgressMode(r.Progress); err != nil {
			return r, err
		}
		rootSpecified, imageSpecified := false, false
		fs.Visit(func(f *flag.Flag) {
			rootSpecified = rootSpecified || f.Name == "rootfs"
			imageSpecified = imageSpecified || f.Name == "image"
		})
		if rootSpecified == imageSpecified || (rootSpecified && r.Config.RootFS == "") || (imageSpecified && r.Config.Image == "") {
			return r, errors.New("run requires exactly one of --rootfs DIRECTORY or --image REFERENCE")
		}
	}
	if r.Action == "doctor" {
		if separator >= 0 {
			return r, errors.New("doctor does not accept a command")
		}
		return r, nil
	}
	if (r.Config.Image == "" && separator < 0) || (separator >= 0 && (len(r.Config.Command) == 0 || r.Config.Command[0] == "")) {
		return r, errors.New("a command is required after --; append a command such as -- /bin/echo hello")
	}
	if r.Config.Image != "" && image.ValidateID(r.Config.Image) != nil {
		if _, err := image.NormalizeReference(r.Config.Image); err != nil {
			return r, err
		}
	}
	if r.Entrypoint != nil && (r.Config.Image == "" || strings.ContainsRune(*r.Entrypoint, 0)) {
		return r, errors.New("--entrypoint requires --image and cannot contain NUL")
	}
	if !hostnamePattern.MatchString(r.Config.Hostname) {
		return r, errors.New("hostname must contain 1-63 letters, digits, or hyphens and start and end with a letter or digit")
	}
	var err error
	r.Config.Memory, err = ParseMemory(memory)
	if err != nil {
		return r, err
	}
	if r.Config.PidsLimit <= 0 {
		return r, errors.New("--pids-limit must be positive")
	}
	if r.Config.Timeout < 0 {
		return r, errors.New("--timeout cannot be negative")
	}
	interactiveSpecified := false
	fs.Visit(func(f *flag.Flag) {
		interactiveSpecified = interactiveSpecified || f.Name == "i" || f.Name == "interactive"
	})
	if !r.Detach && r.Config.RestartPolicy != "" {
		return r, errors.New("--restart requires --detach")
	}
	if r.Detach && (r.Config.Interactive || r.Config.TTY) {
		return r, errors.New("--interactive and --tty require a foreground run")
	}
	if !interactiveSpecified && !r.Detach && !r.Config.TTY {
		r.Config.Interactive = true
	}
	logSpecified := false
	fs.Visit(func(f *flag.Flag) {
		logSpecified = logSpecified || f.Name == "log-max-size" || f.Name == "log-max-files"
	})
	if logSpecified && !r.Detach {
		return r, errors.New("log retention options require --detach")
	}
	if r.Config.LogMaxFiles == 0 {
		return r, errors.New("--log-max-files must be between 1 and 16")
	}
	nameSpecified := false
	fs.Visit(func(f *flag.Flag) { nameSpecified = nameSpecified || f.Name == "name" })
	if nameSpecified {
		if !r.Detach {
			return r, errors.New("--name requires --detach")
		}
		if err := container.ValidateName(r.Name); err != nil {
			return r, err
		}
	}
	r.Config.CPUQuota, err = ParseCPUs(cpus)
	if err != nil {
		return r, err
	}
	// An explicitly empty --user value is invalid rather than a default.
	if user != "" {
		r.Config.User, err = ParseUser(user)
		if err != nil {
			return r, err
		}
	} else {
		var specified bool
		fs.Visit(func(f *flag.Flag) { specified = specified || f.Name == "user" })
		if specified {
			return r, errors.New("--user requires a numeric UID[:GID]")
		}
	}
	workdirSpecified := false
	fs.Visit(func(f *flag.Flag) { workdirSpecified = workdirSpecified || f.Name == "workdir" })
	if workdirSpecified && r.Config.Workdir == "" {
		return r, errors.New("--workdir must be an absolute path")
	}
	if r.Config.Seccomp == "" {
		return r, errors.New("--seccomp requires default or unconfined")
	}
	if r.Config.Rootless {
		r.Config.UserNS = true
		if len(r.Config.UIDMappings) == 0 {
			r.Config.UIDMappings = []config.IDMapping{{ContainerID: 0, HostID: uint32(os.Getuid()), Size: 1}}
		}
		if len(r.Config.GIDMappings) == 0 {
			r.Config.GIDMappings = []config.IDMapping{{ContainerID: 0, HostID: uint32(os.Getgid()), Size: 1}}
		}
	}
	if r.Config.UserNS && r.Detach {
		return r, errors.New("user namespaces currently support foreground runs only")
	}
	if err := r.Config.ValidateRunRequest(); err != nil {
		return r, err
	}
	if r.Config.Workdir != "" {
		r.Config.Workdir = path.Clean(r.Config.Workdir)
	} else if r.Config.Image == "" {
		r.Config.Workdir = "/"
	}
	return r, nil
}
