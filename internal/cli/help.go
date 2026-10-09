package cli

import (
	"fmt"
	"strings"
)

const commandOverview = `Usage: casklet COMMAND [OPTIONS]

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
  system    Inspect VM disk usage
  volume    Create, list, inspect, or remove named data volumes
  image     Pull, import, list, or remove images
  doctor    Check runtime prerequisites and a rootfs template
  help      Show command help
`

const runOptions = `Options:
  -d, --detach   Run in the background and print the container ID
  -i, --interactive  Forward stdin (default for foreground runs without -t)
  -t, --tty      Allocate a terminal; combine with -i as -it for input
  --name         Unique detached name: 1-63 letters, digits, underscores,
                 periods, or hyphens; start alphanumeric; not a full ID
  --rootfs       Linux filesystem template (exclusive with --image)
  --image        Registry NAME[:TAG], NAME@sha256:DIGEST, or local ID/unique ID prefix
  --progress     Image progress: auto, plain, or tty (default: auto)
  --entrypoint   Replace the image entrypoint; empty clears it and its default Cmd
  --hostname     Container hostname: 1-63 alphanumeric/hyphen characters,
                 start and end alphanumeric (default: casklet)
  --memory       Positive bytes or binary k/m/g units (default: 128m)
  --pids-limit   Positive maximum processes and threads (default: 64)
  --cpus         CPU cores, 0 or 0.01-1000, up to 3 decimals (default: 0)
  --config       Strict JSON run options; CLI scalars override config values
  --env-file     Repeatable KEY=VALUE file; explicit --env values take precedence
  --env          KEY=VALUE; repeat; no host environment inheritance
  --workdir      Absolute working directory (image default, otherwise /)
  --user         Numeric UID[:GID]; overrides image User (otherwise 0:0)
  --mount        type=bind,source=/HOST,target=/PATH[,readonly]
                 or type=volume,source=NAME,target=/PATH[,readonly]
                 Repeat for up to 32 mounts; targets cannot overlap
                 Create named volumes first; volumes do not support user namespaces
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
  --health-cmd   Override the image healthcheck with a container shell command
  --no-healthcheck  Disable the image healthcheck
  --health-interval  Delay after each probe, 1ms-24h (image value or 30s)
  --health-timeout   Probe deadline, 1ms-24h (image value or 30s)
  --health-retries   Consecutive failures, 1-1000 (image value or 3)
  --health-start-period  Ignore initial failures, 0s-24h (image value or 0s)
  --health-start-interval  Initial probe delay, 1ms-24h (image value or 5s)
                 Health options require --detach; ps/inspect show health separately
  --restart      no (default), on-failure[:1-1000], always, or unless-stopped
                 Detached only; automatic retries back off from 1s to 30s
  --stop-signal  Linux signal name or 1-64 (image StopSignal, otherwise SIGTERM)
  --stop-timeout Grace before forced shutdown, 0s-1m (default: 5s)
  --log-max-size Bytes or binary k/m/g, 1 KiB-64 MiB (default: 4m; detached only)
  --log-max-files Retained files including current, 1-16 (default: 4; detached only)
                 Combined log size cannot exceed 64 MiB
  --timeout      Nonnegative command duration; 0 disables it (default: 0)

Notes:
  Put -- before COMMAND; options after -- belong to the workload.
  With --image, COMMAND is optional and replaces image Cmd; Entrypoint is retained.
  Missing cached registry images are pulled for the native Linux architecture.
  Image Env, WorkingDir, User, and StopSignal apply unless overridden.
  OCI images require execution without user namespaces; rootless uses directories.
  Terminal options require a foreground run; -it requires a terminal on stdin.
  Detached containers receive no input; stdout and stderr share a retained log.
  Detached image Healthcheck defaults apply automatically; foreground checks are ignored.
  Unhealthy does not restart a container; run -d waits for process startup, not readiness.
  on-failure retries nonzero/unknown exits; :N caps consecutive automatic attempts.
  always/unless-stopped also restart successful exits and resume on VM boot.
  Manual stop pauses retries; only always resumes a manual stop on the next boot.
  Rootfs programs and libraries must match the native Linux architecture.
  Bind sources must not overlap the template or protected runtime paths.
  User namespaces require --network none; maps must include container ID 0,
  contain non-overlapping ranges, and cover the configured container user.
  Rootless runs require a directory rootfs and map only the caller UID/GID.
`

var commandHelp = map[string]string{
	"system": `Usage: casklet system COMMAND

Commands:
  df [--json]  Show guest disk capacity and allocated storage by category

Examples:
  casklet system df
`,
	"system df": `Usage: casklet system df [--json]

Options:
  --json  Print filesystem capacity, low-space status, and storage categories

Notes:
  Counts allocated blocks; includes all retained container roots and logs.
  Does not follow symlinks; excludes mounted subtrees. Live writes may change sampled totals.
  Low space means less than 1 GiB or 10% available on the guest filesystem.
  Host bind data and Mac sparse VM disk allocation are outside these totals.

Examples:
  casklet system df --json
`,
	"image prune": `Usage: casklet image prune [--dry-run]

Options:
  --dry-run  Print eligible image IDs without deleting them

Notes:
  Removes unused cached images, including tagged ones; prints each removed ID.
  Skips active leases and references from any retained container, even stopped ones.
  Containers, logs, data volumes, and host bind data are preserved.
  Each image is rechecked at deletion; a failure may leave a partially completed prune.

Examples:
  casklet image prune --dry-run
  casklet image prune
`,
	"volume": `Usage: casklet volume COMMAND

Commands:
  create NAME   Create an empty named volume (idempotent)
  ls [--json]   List named volumes
  inspect NAME  Show volume metadata as JSON
  export NAME   Write an unused volume as a tar archive to stdout
  restore NAME  Create a new volume from a tar archive on stdin
  rm NAME       Delete an unused volume and its data

Notes:
  Data lives on the VM disk and survives container removal.
  Volume removal refuses active leases and references from retained containers.
  Volumes start empty with root ownership; image contents are not copied into them.

Examples:
  casklet volume create app-data
  casklet run --mount type=volume,source=app-data,target=/data -- /bin/ls /data
`,
	"volume export": `Usage: casklet volume export NAME > ARCHIVE.tar

Options:
  -h, --help  Show this help

Notes:
  Stop all containers using this volume first; retained references are allowed.
  Writes an uncompressed tar stream to stdout, preserving Linux ownership,
  permissions, timestamps, symlinks, and hardlinks. Special files and mounts fail.
  Check the exit status before using the archive. Limit: 16 GiB and 1 million entries.

Examples:
  casklet stop database
  casklet volume export db-data > db-data.tar
`,
	"volume restore": `Usage: casklet volume restore NEW_NAME < ARCHIVE.tar

Options:
  -h, --help  Show this help

Notes:
  Requires a new name; existing volumes are never overwritten.
  Reads an uncompressed tar stream from stdin and atomically publishes the new
  volume after validation. Failure removes staging; unsafe paths and types fail.
  Prints the volume name on success. Limit: 16 GiB and 1 million entries.

Examples:
  casklet volume restore db-restored < db-data.tar
  casklet volume export db-data | casklet volume restore db-copy
`,
	"volume create": `Usage: casklet volume create NAME

Notes:
  Creates an empty VM-local volume; an existing healthy volume is reused.

Examples:
  casklet volume create app-data
`,
	"volume ls": `Usage: casklet volume ls [--json]

Options:
  --json  Print JSON volume metadata

Examples:
  casklet volume ls --json
`,
	"volume inspect": `Usage: casklet volume inspect NAME

Notes:
  Shows the name and creation time as JSON.

Examples:
  casklet volume inspect app-data
`,
	"volume rm": `Usage: casklet volume rm NAME

Notes:
  Deletes the data. Refuses volumes referenced by any retained container or active run.
  Remove referencing containers first; stopping them does not release references.

Examples:
  casklet volume rm app-data
`,
	"exec": `Usage: casklet exec [OPTIONS] ID|NAME -- COMMAND [ARGS...]

Options:
  -i, --interactive  Forward stdin (default: no input)
  -t, --tty          Allocate a terminal; combine with -i as -it for input
  --env-file         Repeatable KEY=VALUE file; explicit --env takes precedence
  --env             Override KEY=VALUE; repeat to add variables
  --workdir         Existing absolute directory (default: container configuration)
  --timeout         Nonnegative command duration; 0 disables it (default: 0)

Notes:
  Flags must precede ID|NAME; put -- before the command.
  The container must be running. Exec inherits its user, isolation, and limits.
  Output goes to the invoking terminal, outside the retained container log.
  Terminal output merges stdout and stderr; -it requires a terminal on stdin.

Examples:
  casklet exec worker -- /bin/echo hello
  casklet exec -it worker -- /bin/sh
`,
	"ps": `Usage: casklet ps [-a|--all] [--json]

Options:
  -a, --all  Include completed containers (default: active containers only)
  --json     Print JSON records with full container IDs

Notes:
  Table IDs show the first 12 hexadecimal characters; JSON retains full IDs.
  Container operations accept an exact name, full ID, or unique ID prefix.

Examples:
  casklet ps
  casklet ps -a --json
`,
	"inspect": `Usage: casklet inspect ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Prints JSON configuration and lifecycle state for a container reference.
  Includes restart_policy, restart_count, restart_at, and stopped_by_user.
  Health includes status, consecutive failures, and five recent probe results.
  Probe output is discarded; unhealthy does not change the restart policy.
  Environment names are shown without values.

Examples:
  casklet inspect worker
  casklet ps -a
`,
	"stats": `Usage: casklet stats [--json] [--interval DURATION] ID|NAME

Options:
  --json      Print JSON statistics with the full container ID
  --interval  CPU sampling interval, 10ms-1m (default: 1s)

Notes:
  Flags must precede ID|NAME. Prints one sample; CPU 100% means one fully used core.
  Table IDs show the first 12 hexadecimal characters; --json retains the full ID.
  Missing live metrics appear as N/A (JSON null).

Examples:
  casklet stats worker
  casklet stats --json --interval 250ms worker
`,
	"logs": `Usage: casklet logs [--tail N] [-f|--follow] ID|NAME

Options:
  --tail        Print the last 0-1000000 lines (default: entire retained log)
  -f, --follow  Follow new output until the container exits

Notes:
  Flags must precede ID|NAME. Logs merge detached stdout and stderr.
  Retention defaults to 16 MiB; configure it at run time with --log-max-*.

Examples:
  casklet logs --tail 20 worker
  casklet logs -f worker
`,
	"stop": `Usage: casklet stop [--timeout DURATION] ID|NAME

Options:
  --timeout  Override graceful shutdown, 0s-1m (default: container configuration)

Notes:
  Flags must precede ID|NAME. Sends the configured stop signal, then SIGKILL after the grace period.
  Keeps the container's files and record for start, inspect, or rm.
  Suppresses automatic restarts until start/restart; always resumes on a new VM boot.

Examples:
  casklet stop worker
  casklet stop --timeout 10s worker
`,
	"wait": `Usage: casklet wait ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Waits for the observed execution to finish, prints its exit code, and returns it.
  Automatic restarts do not redirect an existing wait to a later execution.

Examples:
  casklet wait worker
  casklet inspect worker
`,
	"start": `Usage: casklet start ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Starts a stopped container with its original command and retained filesystem.
  Writes made by earlier executions are preserved; output uses retained logs.

Examples:
  casklet start worker
  casklet logs -f worker
`,
	"restart": `Usage: casklet restart [--timeout DURATION] ID|NAME

Options:
  --timeout  Override graceful shutdown, 0s-1m (default: container configuration)

Notes:
  Flags must precede ID|NAME. Stops and starts the original container command.
  Retained filesystem changes are preserved; an exited container can be restarted.

Examples:
  casklet restart worker
  casklet restart --timeout 10s worker
`,
	"rm": `Usage: casklet rm ID|NAME

Options:
  -h, --help  Show this help

Notes:
  Requires a stopped container. Removes its record, retained files, and logs.
  Stop a running container before removal.

Examples:
  casklet stop worker
  casklet rm worker
`,
	"image": `Usage: casklet image COMMAND

Commands:
  pull REFERENCE    Download or refresh a native Linux OCI/Docker image
  import DIRECTORY  Copy a Linux rootfs into the local image store
  ls [--json]       List cached images and registry references
  rm ID             Remove an unused image
  prune [--dry-run]  Remove images without leases or container references

Notes:
  run --image uses cached names or IDs; an uncached name is pulled automatically.
  image pull explicitly refreshes a mutable tag. Public registries are supported.
  Run casklet image COMMAND --help for the command's options and examples.

Examples:
  casklet image pull redis:8
  casklet image import ./rootfs/busybox
  casklet image ls
`,
	"image pull": `Usage: casklet image pull [--progress auto|plain|tty] REFERENCE

Options:
  --progress  Progress display: auto, plain, or tty (default: auto)
  -h, --help  Show this help

Notes:
  Downloads an anonymous-access OCI/Docker image for the native Linux architecture.
  Accepts NAME[:TAG] or NAME@sha256:DIGEST; Docker Hub and latest are defaults.
  Verifies layer digests and publishes an immutable filesystem with startup defaults.
  Refreshes the cached name and prints its full local sha256: identity.
  Local IDs describe the unpacked rootfs, ownership, and execution defaults;
  they differ from registry manifest digests. No Docker Engine is required.

Examples:
  casklet image pull redis:8
  casklet run -d --name redis --image redis:8
`,
	"image import": `Usage: casklet image import DIRECTORY

Options:
  -h, --help  Show this help

Notes:
  Copies a Linux filesystem into an immutable image; no BusyBox is required.
  Directory imports have no startup defaults; supply a command with run.
  Prints a full sha256: ID; later source changes do not modify the image.

Examples:
  casklet image import ./rootfs/busybox
  casklet image ls --json
`,
	"image ls": `Usage: casklet image ls [--json]

Options:
  --json  Print image records with full sha256: IDs and exact byte sizes

Notes:
  IMAGE shows one row per reference; Docker Hub defaults are omitted, untagged images show <none>.
  ID shows the first 12 hexadecimal characters; SIZE uses decimal B/kB/MB/GB units.
  Use names, full IDs, or unique ID prefixes with run --image.
  image rm accepts full local IDs or unique prefixes, including these table IDs.

Examples:
  casklet image ls
  casklet image ls --json
`,
	"image rm": `Usage: casklet image rm ID

Options:
  -h, --help  Show this help

Notes:
  Accepts a full local image ID or unique hexadecimal prefix, with optional sha256:.
  Ambiguous prefixes fail; use a longer ID. Registry names/tags are not accepted.
  Images in use or referenced by retained containers
  cannot be removed; remove referencing containers first.

Examples:
  casklet image ls
  casklet image rm 406e742c72ac
`,
	"help": `Usage: casklet help [COMMAND [SUBCOMMAND]]

Notes:
  With no topic, lists commands. COMMAND --help and help COMMAND are equivalent.
  Help does not initialize the VM or require runtime privileges.

Examples:
  casklet help run
  casklet help image import
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
		return text + "\nRun casklet COMMAND --help or casklet help COMMAND for syntax and examples.\n", nil
	}
	if topic == "run" {
		syntax := "casklet run (--rootfs DIRECTORY | --image REFERENCE) [OPTIONS] [-- COMMAND [ARGS...]]"
		platformNotes := `  Containers require Linux and a delegated cgroups v2 scope.
  Other than rootless mode, the engine uses sudo and creates a delegated scope.
`
		examples := `  casklet run --rootfs ./rootfs/busybox -- /bin/echo hello
  casklet run -d --name worker --rootfs ./rootfs/busybox -- /bin/sleep 300
`
		if macOS {
			syntax = "casklet run [--rootfs DIRECTORY | --image REFERENCE] [OPTIONS] [-- COMMAND [ARGS...]]"
			platformNotes = `  The default is builtin:busybox; explicit rootfs and bind sources are Mac paths.
  Run as your regular Mac user; requires macOS 13.5+ and Lima 2.0+.
  Install Lima with brew install lima. The client creates or starts its VM as needed.
  Your home is shared; use machine share for additional directories in an existing VM.
  Published ports support 0.0.0.0 and 127.0.0.1 on the Mac; port 22 is reserved.
  Rootless identities refer to the VM user; container state lives on the VM disk.
`
			examples = `  casklet run -- /bin/echo hello
  casklet run -d --name worker -- /bin/sleep 300
  casklet run -it -- /bin/sh
  casklet run -d --name redis --network bridge -p 127.0.0.1:6379:6379 --image redis:8
  casklet run -d --name checked --health-cmd 'test -f /tmp/ready' -- /bin/sleep 300
`
		}
		return "Usage: " + syntax + "\n\n" + runOptions + platformNotes + "\nExamples:\n" + examples, nil
	}
	if topic == "doctor" {
		syntax := "casklet doctor --rootfs DIRECTORY"
		notes := "  Checks the template, Linux isolation capabilities, and delegated cgroups v2.\n  The engine uses sudo and obtains a delegated scope when needed.\n"
		examples := "  casklet doctor --rootfs ./rootfs/busybox\n"
		if macOS {
			syntax = "casklet doctor [--rootfs DIRECTORY]"
			notes = "  Uses builtin:busybox by default. Explicit rootfs paths refer to Mac directories.\n  Creates or starts the product VM and checks runtime capabilities inside it.\n  Run as your regular Mac user; install Lima with brew install lima.\n"
			examples = "  casklet doctor\n  casklet doctor --rootfs ./rootfs/busybox\n"
		}
		return "Usage: " + syntax + "\n\nOptions:\n  --rootfs  Linux filesystem template for the native architecture\n\nNotes:\n" + notes + "\nExamples:\n" + examples, nil
	}
	if text, exists := commandHelp[topic]; exists {
		switch topic {
		case "exec", "inspect", "stats", "stop", "wait", "start", "restart", "rm", "logs":
			text = strings.Replace(text, "\nNotes:\n", "\nNotes:\n  ID accepts a unique hexadecimal prefix; exact names take precedence.\n  Ambiguous prefixes fail; use a longer ID or the exact container name.\n", 1)
		}
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
	return "", fmt.Errorf("unknown help topic %q; run casklet help for available commands", topic)
}
