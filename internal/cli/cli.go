package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/mingo-liu/mini-docker/internal/config"
	"github.com/mingo-liu/mini-docker/internal/container"
	containerruntime "github.com/mingo-liu/mini-docker/internal/runtime"
)

const usage = `Usage:
  mini-docker run --rootfs DIRECTORY [OPTIONS] -- COMMAND [ARGS...]
  mini-docker exec [OPTIONS] ID|NAME -- COMMAND [ARGS...]
  mini-docker ps [-a|--all] [--json]
  mini-docker inspect ID|NAME
  mini-docker stats [--json] [--interval DURATION] ID|NAME
  mini-docker stop ID|NAME
  mini-docker logs [--tail N] [-f|--follow] ID|NAME
  mini-docker rm ID|NAME
  mini-docker doctor --rootfs DIRECTORY
  mini-docker help

Run options:
  -d, --detach   Run in the background and print the container ID
  -i, --interactive  Forward stdin (default for foreground runs without -t)
  -t, --tty      Allocate a terminal; combine with -i as -it for input
  --name         Unique name for a detached container (1-63 letters,
                 digits, underscores, periods, or hyphens; start alphanumeric)
  --rootfs       BusyBox filesystem template (required)
  --hostname     Container hostname (default: mini)
  --memory       Memory limit in bytes or k/m/g units (default: 128m)
  --pids-limit   Maximum number of processes and threads (default: 64)
  --cpus         CPU cores, 0 or 0.01-1000, up to 3 decimals (default: 0)
  --env          Set KEY=VALUE; repeat to add variables (no host inheritance)
  --workdir      Existing absolute working directory (default: /)
  --user         Numeric UID[:GID]; GID defaults to UID (default: 0:0)
  --mount        Bind a directory: type=bind,source=/HOST,target=/PATH[,readonly]
                 Repeat for multiple directories (maximum: 32)
  --read-only    Mount the container root filesystem read-only
  --timeout      Command duration limit; 0 disables it (default: 0)

Exec options:
  -i, --interactive  Forward stdin (default: no input)
  -t, --tty      Allocate a terminal; combine with -i as -it for input
  --env          Override KEY=VALUE; repeat to add variables
  --workdir      Existing absolute directory (default: container configuration)
  --timeout      Command duration limit; 0 disables it (default: 0)

Containers require Linux, root privileges, and a delegated cgroups v2 scope.
Use scripts/run-linux.sh to launch the CLI in a delegated scope.
Management flags must precede the container identifier. ps lists active
containers; --all also includes completed containers. logs defaults to the
entire retained log (maximum 16 MiB); --tail accepts 0-1000000 lines.
stop sends SIGTERM, then SIGKILL after the grace period. rm requires a stopped
container. Detached containers receive no input; stdout and stderr are merged.
run terminal options require a foreground run. -it requires a terminal on stdin.
exec inherits the container user, isolation, and resource limits. Its output
goes to the invoking terminal rather than the retained container log.
exec terminal output merges stdout and stderr. Flags must precede ID|NAME.
inspect prints JSON; environment names are shown without values.
stats prints one sample; --interval is 10ms-1m (default: 1s). CPU 100% means
one fully used core. Missing live metrics are N/A (JSON null).
`

type Request struct {
	Action    string
	Config    config.Config
	Exec      config.Exec
	Detach    bool
	Name      string
	All       bool
	JSON      bool
	Reference string
	Tail      int
	Follow    bool
	Interval  time.Duration
}

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

func Parse(args []string) (Request, error) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return Request{Action: "help"}, nil
	}
	r := Request{Action: args[0]}
	if r.Action == "exec" {
		return parseExec(r, args[1:])
	}
	if r.Action == "ps" || r.Action == "stop" || r.Action == "logs" || r.Action == "rm" || r.Action == "inspect" || r.Action == "stats" {
		return parseManagement(r, args[1:])
	}
	if r.Action != "run" && r.Action != "doctor" {
		return r, fmt.Errorf("unknown command %q", r.Action)
	}
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&r.Config.RootFS, "rootfs", "", "rootfs template")
	var memory, cpus, user string
	if r.Action == "run" {
		fs.BoolVar(&r.Detach, "detach", false, "run in the background")
		fs.BoolVar(&r.Detach, "d", false, "run in the background")
		fs.BoolVar(&r.Config.Interactive, "interactive", false, "forward stdin")
		fs.BoolVar(&r.Config.Interactive, "i", false, "forward stdin")
		fs.BoolVar(&r.Config.TTY, "tty", false, "allocate a terminal")
		fs.BoolVar(&r.Config.TTY, "t", false, "allocate a terminal")
		fs.StringVar(&r.Name, "name", "", "detached container name")
		fs.StringVar(&r.Config.Hostname, "hostname", "mini", "hostname")
		fs.StringVar(&memory, "memory", "128m", "memory limit")
		fs.Int64Var(&r.Config.PidsLimit, "pids-limit", 64, "process and thread limit")
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
	if r.Action == "run" {
		options = expandTerminalFlags(fs, options)
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
	interactiveSpecified := false
	fs.Visit(func(f *flag.Flag) {
		interactiveSpecified = interactiveSpecified || f.Name == "i" || f.Name == "interactive"
	})
	if r.Detach && (r.Config.Interactive || r.Config.TTY) {
		return r, errors.New("--interactive and --tty require a foreground run")
	}
	if !interactiveSpecified && !r.Detach && !r.Config.TTY {
		r.Config.Interactive = true
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
	if r.Config.Workdir == "" {
		return r, errors.New("--workdir must be an absolute path")
	}
	if err := r.Config.ValidateExecution(); err != nil {
		return r, err
	}
	r.Config.Workdir = path.Clean(r.Config.Workdir)
	return r, nil
}

// expandTerminalFlags accepts the common -it spelling without changing flag
// values or positional arguments. Other short flag combinations stay invalid.
func expandTerminalFlags(fs *flag.FlagSet, options []string) []string {
	expanded := make([]string, 0, len(options)+1)
	for i := 0; i < len(options); i++ {
		arg := options[i]
		if arg == "-it" || arg == "-ti" {
			expanded = append(expanded, "-i", "-t")
			continue
		}
		expanded = append(expanded, arg)
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			expanded = append(expanded, options[i+1:]...)
			break
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(name)
		if f == nil || hasValue {
			continue
		}
		boolean, ok := f.Value.(interface{ IsBoolFlag() bool })
		if ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 < len(options) {
			i++
			expanded = append(expanded, options[i])
		}
	}
	return expanded
}

func parseManagement(r Request, args []string) (Request, error) {
	fs := flag.NewFlagSet(r.Action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch r.Action {
	case "ps":
		fs.BoolVar(&r.All, "all", false, "include completed containers")
		fs.BoolVar(&r.All, "a", false, "include completed containers")
		fs.BoolVar(&r.JSON, "json", false, "print JSON records")
	case "stats":
		fs.BoolVar(&r.JSON, "json", false, "print JSON statistics")
		fs.DurationVar(&r.Interval, "interval", time.Second, "CPU sampling interval")
	case "logs":
		r.Tail = -1
		fs.IntVar(&r.Tail, "tail", -1, "maximum number of trailing lines")
		fs.BoolVar(&r.Follow, "follow", false, "follow log output")
		fs.BoolVar(&r.Follow, "f", false, "follow log output")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Request{Action: "help"}, nil
		}
		return r, err
	}
	if r.Action == "ps" {
		if fs.NArg() != 0 {
			return r, errors.New("ps does not accept positional arguments")
		}
		return r, nil
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return r, fmt.Errorf("%s requires exactly one container ID or name; flags must precede it", r.Action)
	}
	if r.Action == "stats" {
		if err := container.ValidateStatsInterval(r.Interval); err != nil {
			return r, err
		}
	}
	r.Reference = fs.Arg(0)
	if strings.ContainsAny(r.Reference, "/\\\x00") || r.Reference == "." || r.Reference == ".." {
		return r, errors.New("invalid container ID or name")
	}
	if r.Action == "logs" {
		explicitTail := false
		fs.Visit(func(f *flag.Flag) { explicitTail = explicitTail || f.Name == "tail" })
		if (explicitTail && r.Tail < 0) || r.Tail > 1000000 {
			return r, errors.New("--tail must be between 0 and 1000000")
		}
	}
	return r, nil
}

func parseExec(r Request, args []string) (Request, error) {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&r.Exec.Interactive, "interactive", false, "forward stdin")
	fs.BoolVar(&r.Exec.Interactive, "i", false, "forward stdin")
	fs.BoolVar(&r.Exec.TTY, "tty", false, "allocate a terminal")
	fs.BoolVar(&r.Exec.TTY, "t", false, "allocate a terminal")
	fs.Func("env", "environment assignment", func(value string) error {
		r.Exec.Env = append(r.Exec.Env, value)
		return nil
	})
	fs.StringVar(&r.Exec.Workdir, "workdir", "", "working directory")
	fs.DurationVar(&r.Exec.Timeout, "timeout", 0, "command timeout")
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			separator = i
			break
		}
	}
	options := args
	if separator >= 0 {
		options = args[:separator]
		r.Exec.Command = append([]string(nil), args[separator+1:]...)
	}
	if err := fs.Parse(expandTerminalFlags(fs, options)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Request{Action: "help"}, nil
		}
		return r, err
	}
	var workdirSpecified bool
	fs.Visit(func(f *flag.Flag) {
		workdirSpecified = workdirSpecified || f.Name == "workdir"
	})
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return r, errors.New("exec requires exactly one container ID or name before --; flags must precede it")
	}
	r.Reference = fs.Arg(0)
	if strings.ContainsAny(r.Reference, "/\\\x00") || r.Reference == "." || r.Reference == ".." {
		return r, errors.New("invalid container ID or name")
	}
	if separator < 0 {
		return r, errors.New("a command is required after --")
	}
	if workdirSpecified && r.Exec.Workdir == "" {
		return r, errors.New("--workdir must be an absolute path")
	}
	if err := r.Exec.Validate(); err != nil {
		return r, err
	}
	if r.Exec.Workdir != "" {
		r.Exec.Workdir = path.Clean(r.Exec.Workdir)
	}
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
	case "ps", "stop", "logs", "rm", "inspect", "stats":
		return executeManagement(r, stdout, stderr)
	case "exec":
		signals := make(chan os.Signal, 16)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
		defer signal.Stop(signals)
		code, err := container.Exec(context.Background(), r.Reference, r.Exec, stdin, stdout, stderr, signals)
		if err != nil {
			fmt.Fprintf(stderr, "mini-docker: %v\n", err)
		}
		return code
	default:
		if r.Detach {
			return executeManagement(r, stdout, stderr)
		}
		code, err := containerruntime.Run(r.Config, stdin, stdout, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "mini-docker: %v\n", err)
		}
		return code
	}
}

func executeManagement(r Request, stdout, stderr io.Writer) int {
	// Keep the signal itself as well as cancellation so shell exit codes retain
	// the distinction between an interrupt and a termination request.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	operationCtx := ctx
	if r.Action == "run" {
		var cancel context.CancelFunc
		operationCtx, cancel = context.WithTimeout(ctx, 100*time.Second)
		defer cancel()
	}
	var err error
	switch r.Action {
	case "run":
		var record container.Record
		record, err = container.Start(operationCtx, r.Config, r.Name)
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "ps":
		var records []container.Record
		records, err = container.List(operationCtx, r.All)
		if err == nil {
			err = writeRecords(stdout, records, r.JSON)
		}
	case "inspect":
		var inspection container.Inspection
		inspection, err = container.Inspect(operationCtx, r.Reference)
		if err == nil {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			err = encoder.Encode(inspection)
		}
	case "stats":
		var stats container.Statistics
		stats, err = container.Stats(operationCtx, r.Reference, r.Interval)
		if err == nil {
			err = writeStats(stdout, stats, r.JSON)
		}
	case "stop":
		var record container.Record
		record, err = container.Stop(operationCtx, r.Reference)
		if err == nil {
			_, err = fmt.Fprintln(stdout, record.ID)
		}
	case "logs":
		err = container.Logs(operationCtx, r.Reference, r.Tail, r.Follow, stdout)
	case "rm":
		err = container.Remove(operationCtx, r.Reference)
		if err == nil {
			_, err = fmt.Fprintln(stdout, r.Reference)
		}
	}
	if ctx.Err() != nil {
		// This context is canceled only by a signal, which is also delivered
		// to the buffered channel. Receiving it avoids a notification race.
		if <-signals == syscall.SIGTERM {
			return 143
		}
		return 130
	}
	if err != nil {
		fmt.Fprintf(stderr, "mini-docker: %v\n", err)
		return 125
	}
	return 0
}

func writeRecords(out io.Writer, records []container.Record, asJSON bool) error {
	if asJSON {
		if records == nil {
			records = []container.Record{}
		}
		return json.NewEncoder(out).Encode(records)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tSTATUS\tEXIT\tCREATED\tCOMMAND"); err != nil {
		return err
	}
	for _, record := range records {
		exit := "-"
		if record.ExitCode != nil {
			exit = strconv.Itoa(*record.ExitCode)
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", record.ID, record.Name, record.State, exit, record.CreatedAt.UTC().Format(time.RFC3339), displayCommand(record.Command)); err != nil {
			return err
		}
	}
	return w.Flush()
}

func displayCommand(command []string) string {
	parts := make([]string, len(command))
	for i, arg := range command {
		if arg == "" || strings.IndexFunc(arg, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			parts[i] = strconv.Quote(arg)
		} else {
			parts[i] = arg
		}
	}
	return strings.Join(parts, " ")
}

func writeStats(out io.Writer, stats container.Statistics, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(stats)
	}
	memory, cpu := "N/A", "N/A"
	if stats.MemoryBytes != nil {
		memory = strconv.FormatUint(*stats.MemoryBytes, 10)
	}
	if stats.CPUPercent != nil {
		cpu = fmt.Sprintf("%.2f%%", *stats.CPUPercent)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tSTATUS\tMEMORY (BYTES)\tLIMIT (BYTES)\tCPU"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", stats.ID, stats.Name, stats.State, memory, stats.MemoryLimitBytes, cpu); err != nil {
		return err
	}
	return w.Flush()
}
