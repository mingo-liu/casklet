package cli

const usage = `Usage:
  mdocker run (--rootfs DIRECTORY | --image ID) [OPTIONS] -- COMMAND [ARGS...]
  mdocker image import DIRECTORY
  mdocker image ls [--json]
  mdocker image rm ID
  mdocker exec [OPTIONS] ID|NAME -- COMMAND [ARGS...]
  mdocker ps [-a|--all] [--json]
  mdocker inspect ID|NAME
  mdocker stats [--json] [--interval DURATION] ID|NAME
  mdocker wait ID|NAME
  mdocker start ID|NAME
  mdocker restart [--timeout DURATION] ID|NAME
  mdocker stop [--timeout DURATION] ID|NAME
  mdocker logs [--tail N] [-f|--follow] ID|NAME
  mdocker rm ID|NAME
  mdocker doctor --rootfs DIRECTORY
  mdocker help

Run options:
  -d, --detach   Run in the background and print the container ID
  -i, --interactive  Forward stdin (default for foreground runs without -t)
  -t, --tty      Allocate a terminal; combine with -i as -it for input
  --name         Unique name for a detached container (1-63 letters,
                 digits, underscores, periods, or hyphens; start alphanumeric)
  --rootfs       BusyBox filesystem template (exclusive with --image)
  --image        Full sha256: image ID from the local image store
  --hostname     Container hostname (default: mini)
  --memory       Memory limit in bytes or k/m/g units (default: 128m)
  --pids-limit   Maximum number of processes and threads (default: 64)
  --cpus         CPU cores, 0 or 0.01-1000, up to 3 decimals (default: 0)
  --env          Set KEY=VALUE; repeat to add variables (no host inheritance)
  --workdir      Existing absolute working directory (default: /)
  --user         Numeric UID[:GID]; GID defaults to UID (default: 0:0)
  --mount        Bind a directory: type=bind,source=/HOST,target=/PATH[,readonly]
                 Repeat for multiple directories (maximum: 32)
  --network      none (loopback only, default) or bridge (IPv4 connectivity)
  --dns          IPv4 DNS server; repeat up to three times (bridge only)
  -p, --publish  [HOST_IP:]HOST_PORT:CONTAINER_PORT[/tcp|udp]; repeat (bridge only)
  --seccomp      default (deny dangerous syscalls) or unconfined
  --userns       Use a user namespace (foreground only; requires UID/GID maps)
  --uid-map      CONTAINER_ID:HOST_ID:SIZE; repeat for independent ranges
  --gid-map      CONTAINER_ID:HOST_ID:SIZE; repeat for independent ranges
  --rootless     Run as the caller with container 0:0 mapped to caller IDs
  --read-only    Mount the container root filesystem read-only
  --stop-timeout Grace before forced shutdown, 0s-1m (default: 5s)
  --log-max-size Maximum log file size in bytes or k/m/g (default: 4m; detached only)
  --log-max-files Retained log files including current (default: 4; detached only)
  --timeout      Command duration limit; 0 disables it (default: 0)

Exec options:
  -i, --interactive  Forward stdin (default: no input)
  -t, --tty      Allocate a terminal; combine with -i as -it for input
  --env          Override KEY=VALUE; repeat to add variables
  --workdir      Existing absolute directory (default: container configuration)
  --timeout      Command duration limit; 0 disables it (default: 0)

Containers require Linux and a delegated cgroups v2 scope.
Rootless foreground runs use a user scope without sudo; other modes require root.
mdocker automatically uses sudo when needed and creates a delegated scope
for foreground runs. Help and invalid arguments do not require privileges.
Management flags must precede the container identifier. ps lists active
containers; --all also includes completed containers. logs defaults to the
entire retained log (default maximum 16 MiB); --tail accepts 0-1000000 lines.
stop sends SIGTERM, then SIGKILL after the configured grace period. rm requires a stopped
container. Detached containers receive no input; stdout and stderr are merged.
run terminal options require a foreground run. -it requires a terminal on stdin.
exec inherits the container user, isolation, and resource limits. Its output
goes to the invoking terminal rather than the retained container log.
exec terminal output merges stdout and stderr. Flags must precede ID|NAME.
inspect prints JSON; environment names are shown without values.
stats prints one sample; --interval is 10ms-1m (default: 1s). CPU 100% means
one fully used core. Missing live metrics are N/A (JSON null).
`
