package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/mingo-liu/mini-docker/internal/config"
	containerruntime "github.com/mingo-liu/mini-docker/internal/runtime"
)

const usage = `Usage:
  mini-docker run --rootfs DIRECTORY [OPTIONS] -- COMMAND [ARGS...]
  mini-docker doctor --rootfs DIRECTORY
  mini-docker help

Run options:
  --rootfs       BusyBox filesystem template (required)
  --hostname     Container hostname (default: mini)
  --memory       Memory limit in bytes or k/m/g units (default: 128m)
  --pids-limit   Maximum number of processes and threads (default: 64)
  --cpus         CPU cores, 0 or 0.01-1000, up to 3 decimals (default: 0)
  --env          Set KEY=VALUE; repeat to add variables (no host inheritance)
  --workdir      Existing absolute working directory (default: /)
  --user         Numeric UID[:GID]; GID defaults to UID (default: 0:0)
  --read-only    Mount the container root filesystem read-only
  --timeout      Command duration limit; 0 disables it (default: 0)

Containers require Linux, root privileges, and a delegated cgroups v2 scope.
Use scripts/run-linux.sh to launch the CLI in a delegated scope.
`

type Request struct {
	Action string
	Config config.Config
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func Parse(args []string) (Request, error) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return Request{Action: "help"}, nil
	}
	r := Request{Action: args[0]}
	if r.Action != "run" && r.Action != "doctor" {
		return r, fmt.Errorf("unknown command %q", r.Action)
	}
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&r.Config.RootFS, "rootfs", "", "rootfs template")
	var memory, cpus, user string
	if r.Action == "run" {
		fs.StringVar(&r.Config.Hostname, "hostname", "mini", "hostname")
		fs.StringVar(&memory, "memory", "128m", "memory limit")
		fs.Int64Var(&r.Config.PidsLimit, "pids-limit", 64, "process and thread limit")
		fs.DurationVar(&r.Config.Timeout, "timeout", 0, "command timeout")
		fs.StringVar(&cpus, "cpus", "0", "CPU cores")
		fs.Func("env", "environment assignment", func(value string) error {
			r.Config.Env = append(r.Config.Env, value)
			return nil
		})
		fs.StringVar(&r.Config.Workdir, "workdir", "/", "working directory")
		fs.StringVar(&user, "user", "", "numeric user and group IDs")
		fs.BoolVar(&r.Config.ReadOnly, "read-only", false, "read-only root filesystem")
	}
	options := args[1:]
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
	if err := fs.Parse(options); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Request{Action: "help"}, nil
		}
		return r, err
	}
	if fs.NArg() != 0 {
		return r, errors.New("unexpected positional argument before --")
	}
	if r.Config.RootFS == "" {
		return r, errors.New("--rootfs is required")
	}
	if r.Action == "doctor" {
		if separator >= 0 {
			return r, errors.New("doctor does not accept a command")
		}
		return r, nil
	}
	if separator < 0 || len(r.Config.Command) == 0 || r.Config.Command[0] == "" {
		return r, errors.New("a command is required after --")
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
	if r.Config.Workdir == "" {
		return r, errors.New("--workdir must be an absolute path")
	}
	if err := r.Config.ValidateExecution(); err != nil {
		return r, err
	}
	r.Config.Workdir = path.Clean(r.Config.Workdir)
	return r, nil
}

func ParseMemory(value string) (int64, error) {
	original := value
	value = strings.ToLower(value)
	multiplier := int64(1)
	if len(value) > 0 {
		switch value[len(value)-1] {
		case 'k':
			multiplier = 1 << 10
		case 'm':
			multiplier = 1 << 20
		case 'g':
			multiplier = 1 << 30
		}
		if multiplier != 1 {
			value = value[:len(value)-1]
		}
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("invalid memory limit %q: use a positive integer with an optional k, m, or g suffix", original)
	}
	return n * multiplier, nil
}

var cpuPattern = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]{1,3})?|\.[0-9]{1,3})$`)
var identityPattern = regexp.MustCompile(`^[0-9]+$`)

// ParseCPUs converts a decimal CPU count to an exact cgroups v2 quota.
func ParseCPUs(value string) (int64, error) {
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("invalid CPU limit %q: use 0 or 0.01-1000 cores with up to 3 decimal places", value)
	}
	if !cpuPattern.MatchString(value) {
		return invalid()
	}
	whole, fractional, _ := strings.Cut(value, ".")
	if whole == "" {
		whole = "0"
	}
	cores, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || cores > 1000 {
		return invalid()
	}
	fractional += strings.Repeat("0", 3-len(fractional))
	fraction, err := strconv.ParseInt(fractional, 10, 64)
	if err != nil {
		return invalid()
	}
	quota := cores*config.CPUPeriod + fraction*100
	if quota > config.MaxCPUQuota || (quota > 0 && quota < config.MinCPUQuota) {
		return invalid()
	}
	return quota, nil
}

// ParseUser accepts only numeric IDs; it does not consult host user databases.
func ParseUser(value string) (*config.User, error) {
	uidText, gidText, hasGroup := strings.Cut(value, ":")
	if !hasGroup {
		gidText = uidText
	}
	parseID := func(text string) (uint32, error) {
		if !identityPattern.MatchString(text) {
			return 0, errors.New("invalid numeric ID")
		}
		id, err := strconv.ParseUint(text, 10, 32)
		if err != nil || id == math.MaxUint32 {
			return 0, errors.New("numeric ID is out of range")
		}
		return uint32(id), nil
	}
	uid, uidErr := parseID(uidText)
	gid, gidErr := parseID(gidText)
	if uidErr != nil || gidErr != nil {
		return nil, fmt.Errorf("invalid user %q: use numeric UID[:GID] between 0 and 4294967294", value)
	}
	return &config.User{UID: uid, GID: gid}, nil
}

func Execute(args []string, stdin, stdout, stderr *os.File) int {
	r, err := Parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "mini-docker: %v\n", err)
		return 125
	}
	switch r.Action {
	case "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "doctor":
		if err := containerruntime.Check(r.Config.RootFS); err != nil {
			fmt.Fprintf(stderr, "mini-docker: %v\n", err)
			return 125
		}
		fmt.Fprintln(stdout, "All required runtime capabilities are available.")
		return 0
	default:
		code, err := containerruntime.Run(r.Config, stdin, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "mini-docker: %v\n", err)
		}
		return code
	}
}
