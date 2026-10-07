package cli

import (
	"fmt"
	"strings"
)

const commandOverview = `Usage: mdocker COMMAND [OPTIONS]

Commands:
  run       Run a command in a new container
  exec      Run a command in a running container
  ps        List containers
  inspect   Show a container's configuration and lifecycle as JSON
  stats     Sample a container's memory and CPU usage
  logs      Read or follow retained container output
  stop      Stop a container gracefully
  wait      Wait for a container's exit status
  start     Start a stopped container with its retained files
  restart   Stop and start a container with its retained files
  rm        Remove a stopped container
  image     Import, list, or remove local images
  doctor    Check runtime prerequisites and a rootfs template
  help      Show command help
`

const runOptions = `Options:
  -d, --detach   Run in the background and print the container ID
  -i, --interactive  Forward stdin (default for foreground runs without -t)
  -t, --tty      Allocate a terminal; combine with -i as -it for input
  --name         Unique detached name: 1-63 letters, digits, underscores,
                 periods, or hyphens; start alphanumeric; not a full ID
  --rootfs       BusyBox filesystem template (exclusive with --image)
  --image        Full sha256: image ID from the local image store
  --hostname     Container hostname: 1-63 alphanumeric/hyphen characters,
                 start and end alphanumeric (default: mini)
  --memory       Positive bytes or binary k/m/g units (default: 128m)
  --pids-limit   Positive maximum processes and threads (default: 64)
  --cpus         CPU cores, 0 or 0.01-1000, up to 3 decimals (default: 0)
  --env          KEY=VALUE; repeat; no host environment inheritance
  --workdir      Existing absolute working directory (default: /)
  --user         Numeric UID[:GID]; GID defaults to UID (default: 0:0)
  --mount        type=bind,source=/HOST,target=/PATH[,readonly]
                 Repeat for up to 32 existing directories; targets cannot overlap
  --network      none (loopback only, default) or bridge (IPv4 connectivity)
  --dns          Unicast, non-loopback IPv4 DNS; repeat up to 3 times (bridge only)
  -p, --publish  [HOST_IP:]HOST_PORT:CONTAINER_PORT[/tcp|udp] (bridge only)
                 Repeat up to 32 times; ports 1-65535; host bindings cannot overlap
  --seccomp      default (deny dangerous syscalls) or unconfined
  --userns       Use a user namespace (foreground only; requires UID/GID maps)
  --uid-map      CONTAINER_ID:HOST_ID:SIZE; repeat for independent ranges
  --gid-map      CONTAINER_ID:HOST_ID:SIZE; repeat for independent ranges
  --rootless     Map container 0:0 to caller IDs without sudo (foreground only)
  --read-only    Mount the root filesystem read-only; /tmp stays writable
  --stop-timeout Grace before forced shutdown, 0s-1m (default: 5s)
  --log-max-size Bytes or binary k/m/g, 1 KiB-64 MiB (default: 4m; detached only)
  --log-max-files Retained files including current, 1-16 (default: 4; detached only)
                 Combined log size cannot exceed 64 MiB
  --timeout      Nonnegative command duration; 0 disables it (default: 0)

Notes:
  Put -- before COMMAND; options after -- belong to the workload.
  Terminal options require a foreground run; -it requires a terminal on stdin.
  Detached containers receive no input; stdout and stderr share a retained log.
  Rootfs templates must contain static Linux BusyBox for the native architecture.
  Bind sources must not overlap the template or protected runtime paths.
  User namespaces require --network none; maps must include container ID 0,
  contain non-overlapping ranges, and cover the configured container user.
  Rootless runs require a directory rootfs and map only the caller UID/GID.
`

var commandHelp = map[string]string{
	"exec": `Usage: mdocker exec [OPTIONS] ID|NAME -- COMMAND [ARGS...]

Options:
  -i, --interactive  Forward stdin (default: no input)
  -t, --tty          Allocate a terminal; combine with -i as -it for input
  --env             Override KEY=VALUE; repeat to add variables
  --workdir         Existing absolute directory (default: container configuration)
  --timeout         Nonnegative command duration; 0 disables it (default: 0)

Notes:
  Flags must precede ID|NAME; put -- before the command.
  The container must be running. Exec inherits its user, isolation, and limits.
  Output goes to the invoking terminal, outside the retained container log.
  Terminal output merges stdout and stderr; -it requires a terminal on stdin.

Examples:
  mdocker exec worker -- /bin/echo hello
  mdocker exec -it worker -- /bin/sh
`,
	"ps": `Usage: mdocker ps [-a|--all] [--json]

Options:
  -a, --all  Include completed containers (default: active containers only)
  --json     Print JSON records

Notes:
  Container operations accept a full ID or exact name from this list.

Examples:
  mdocker ps
  mdocker ps -a --json
`,
	"inspect": `Usage: mdocker inspect ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Prints JSON configuration and lifecycle state for a full ID or exact name.
  Environment names are shown without values.

Examples:
  mdocker inspect worker
  mdocker ps -a
`,
	"stats": `Usage: mdocker stats [--json] [--interval DURATION] ID|NAME

Options:
  --json      Print JSON statistics
  --interval  CPU sampling interval, 10ms-1m (default: 1s)

Notes:
  Flags must precede ID|NAME. Prints one sample; CPU 100% means one fully used core.
  Missing live metrics appear as N/A (JSON null).

Examples:
  mdocker stats worker
  mdocker stats --json --interval 250ms worker
`,
	"logs": `Usage: mdocker logs [--tail N] [-f|--follow] ID|NAME

Options:
  --tail        Print the last 0-1000000 lines (default: entire retained log)
  -f, --follow  Follow new output until the container exits

Notes:
  Flags must precede ID|NAME. Logs merge detached stdout and stderr.
  Retention defaults to 16 MiB; configure it at run time with --log-max-*.

Examples:
  mdocker logs --tail 20 worker
  mdocker logs -f worker
`,
	"stop": `Usage: mdocker stop [--timeout DURATION] ID|NAME

Options:
  --timeout  Override graceful shutdown, 0s-1m (default: container configuration)

Notes:
  Flags must precede ID|NAME. Sends SIGTERM, then SIGKILL after the grace period.
  Keeps the container's files and record for start, inspect, or rm.

Examples:
  mdocker stop worker
  mdocker stop --timeout 10s worker
`,
	"wait": `Usage: mdocker wait ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Waits for the observed execution to finish, prints its exit code, and returns it.

Examples:
  mdocker wait worker
  mdocker inspect worker
`,
	"start": `Usage: mdocker start ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Starts a stopped container with its original command and retained filesystem.
  Writes made by earlier executions are preserved; output uses retained logs.

Examples:
  mdocker start worker
  mdocker logs -f worker
`,
	"restart": `Usage: mdocker restart [--timeout DURATION] ID|NAME

Options:
  --timeout  Override graceful shutdown, 0s-1m (default: container configuration)

Notes:
  Flags must precede ID|NAME. Stops and starts the original container command.
  Retained filesystem changes are preserved; an exited container can be restarted.

Examples:
  mdocker restart worker
  mdocker restart --timeout 10s worker
`,
	"rm": `Usage: mdocker rm ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Requires a stopped container. Removes its record, retained files, and logs.
  Stop a running container before removal.

Examples:
  mdocker stop worker
  mdocker rm worker
`,
	"image": `Usage: mdocker image COMMAND

Commands:
  import DIRECTORY  Copy a BusyBox template into the local image store
  ls [--json]       List imported images
  rm ID             Remove an unused image

Notes:
  Images are local snapshots without registry or layer support.
  Run mdocker image COMMAND --help for the command's options and examples.

Examples:
  mdocker image import ./rootfs/busybox
  mdocker image ls
`,
	"image import": `Usage: mdocker image import DIRECTORY

Options:
  -h, --help  Show this help

Notes:
  Copies a native-architecture static BusyBox template into an immutable image.
  Prints a full sha256: ID; later source changes do not modify the image.

Examples:
  mdocker image import ./rootfs/busybox
  mdocker image ls --json
`,
	"image ls": `Usage: mdocker image ls [--json]

Options:
  --json  Print JSON image records

Notes:
  Lists local imported images. Use their full sha256: IDs with run --image.

Examples:
  mdocker image ls
  mdocker image ls --json
`,
	"image rm": `Usage: mdocker image rm ID

Options:
  -h, --help  Show this help

Notes:
  Requires a full sha256: ID. Images in use or referenced by retained containers
  cannot be removed; remove referencing containers first.

Examples:
  mdocker image ls
  mdocker image rm sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`,
	"help": `Usage: mdocker help [COMMAND [SUBCOMMAND]]

Notes:
  With no topic, lists commands. COMMAND --help and help COMMAND are equivalent.
  Help does not initialize the VM or require runtime privileges.

Examples:
  mdocker help run
  mdocker help image import
`,
}

func hostHelpTopic(topic string) bool {
	switch topic {
	case "machine", "machine init", "machine start", "machine stop", "machine status", "machine share", "rootfs":
		return true
	}
	return false
}

func helpRequest(topics []string) (Request, error) {
	topic := strings.Join(topics, " ")
	if topic == "" && len(topics) == 0 {
		return Request{Action: "help"}, nil
	}
	if _, exists := commandHelp[topic]; exists || topic == "run" || topic == "doctor" || hostHelpTopic(topic) {
		return Request{Action: "help", HelpTopic: topic}, nil
	}
	return Request{}, fmt.Errorf("unknown help topic %q; choose one command, optionally followed by its subcommand", topic)
}

func scopedUsage(topic string, macOS bool) (string, error) {
	if topic == "" {
		text := commandOverview
		if macOS {
			text += "  machine   Manage the product VM and shared directories\n  rootfs    Export the built-in BusyBox template to a Mac directory\n"
		}
		return text + "\nRun mdocker COMMAND --help or mdocker help COMMAND for syntax and examples.\n", nil
	}
	if topic == "run" {
		syntax := "mdocker run (--rootfs DIRECTORY | --image ID) [OPTIONS] -- COMMAND [ARGS...]"
		platformNotes := `  Containers require Linux and a delegated cgroups v2 scope.
  Other than rootless mode, the engine uses sudo and creates a delegated scope.
`
		examples := `  mdocker run --rootfs ./rootfs/busybox -- /bin/echo hello
  mdocker run -d --name worker --rootfs ./rootfs/busybox -- /bin/sleep 300
`
		if macOS {
			syntax = "mdocker run [--rootfs DIRECTORY | --image ID] [OPTIONS] -- COMMAND [ARGS...]"
			platformNotes = `  The default is builtin:busybox; explicit rootfs and bind sources are Mac paths.
  Run as your regular Mac user; requires macOS 13.5+ and Lima 2.0+.
  Install Lima with brew install lima. The client creates or starts its VM as needed.
  Your home is shared; use machine share for additional directories in an existing VM.
  Published ports support 0.0.0.0 and 127.0.0.1 on the Mac; port 22 is reserved.
  Rootless identities refer to the VM user; container state lives on the VM disk.
`
			examples = `  mdocker run -- /bin/echo hello
  mdocker run -d --name worker -- /bin/sleep 300
  mdocker run -it -- /bin/sh
`
		}
		return "Usage: " + syntax + "\n\n" + runOptions + platformNotes + "\nExamples:\n" + examples, nil
	}
	if topic == "doctor" {
		syntax := "mdocker doctor --rootfs DIRECTORY"
		notes := "  Checks the template, Linux isolation capabilities, and delegated cgroups v2.\n  The engine uses sudo and obtains a delegated scope when needed.\n"
		examples := "  mdocker doctor --rootfs ./rootfs/busybox\n"
		if macOS {
			syntax = "mdocker doctor [--rootfs DIRECTORY]"
			notes = "  Uses builtin:busybox by default. Explicit rootfs paths refer to Mac directories.\n  Creates or starts the product VM and checks runtime capabilities inside it.\n  Run as your regular Mac user; install Lima with brew install lima.\n"
			examples = "  mdocker doctor\n  mdocker doctor --rootfs ./rootfs/busybox\n"
		}
		return "Usage: " + syntax + "\n\nOptions:\n  --rootfs  Static BusyBox template for the native Linux architecture\n\nNotes:\n" + notes + "\nExamples:\n" + examples, nil
	}
	if text, exists := commandHelp[topic]; exists {
		if macOS {
			switch topic {
			case "inspect":
				text = strings.Replace(text, "  Environment names", "  Mac addresses and canonical source paths appear in config; guest_resources\n  retains the VM's rootfs, bind sources, and published addresses for diagnostics.\n  Environment names", 1)
			case "image import":
				text = strings.Replace(text, "  Copies a native-architecture", "  DIRECTORY is a shared Mac path. The image store lives on the VM disk.\n  Copies a native-architecture", 1)
			}
		}
		return text, nil
	}
	return "", fmt.Errorf("unknown help topic %q; run mdocker help for available commands", topic)
}
