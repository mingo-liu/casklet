# Usage guide

[Quick start](../README.md) · [Development](development.md) · [Architecture](../ARCHITECTURE.md)

Run `casklet help` for the command overview. Each command has a focused reference:

```sh
casklet run --help
casklet help logs
casklet help image import
casklet machine share --help
```

`casklet help COMMAND` and `casklet COMMAND --help` show the same reference,
including options, defaults, constraints, and examples. Help works without Lima
and does not create or start the VM. Management flags precede
the container ID or exact name. Put `--` before an explicit `run` or `exec` command;
`run --image` can omit the command and separator to use image startup defaults.
Host environment variables are never inherited by workloads.
Local rootfs structure, bind sources, supported published addresses, and occupied
Mac ports are checked before VM startup. Unshared paths are rejected before
creation or startup; for a new VM the error suggests `machine init --mount`,
and for an existing VM it suggests the stop/share/start recovery flow.

## macOS execution environment

Run every public command on the Mac as your regular user. The client creates or
starts the `casklet-runtime` Lima VM as needed and installs its bundled
casklet Linux engine. The default rootfs is the VM's static BusyBox template:

```sh
casklet run -- /bin/sh
casklet doctor
casklet rootfs ./rootfs/busybox
```

An explicit `--rootfs DIRECTORY`, `image import DIRECTORY`, or bind source refers
to a Mac directory. Relative template/import paths resolve against the Mac's
current directory. Host symlinks are resolved before translating a path into its
shared guest location. Workload command arguments, environment values, and
container paths are passed unchanged; shell expansion happens only when you
explicitly run a shell in the container.

The home directory is shared writable with the VM. Additional directories can
be configured before first use with `casklet machine init --mount DIRECTORY`.
To add a directory to an existing VM, stop it, add the share, and start it:

```sh
casklet machine stop
casklet machine share /Volumes/Projects
casklet machine start
```

Stopping the VM terminates running containers and preserves their files. Restart
retained containers with `casklet start NAME`. Adding a share preserves the VM
disk, resources, forwarding rules, and existing shares. Sharing an already
covered writable directory is a no-op. Overlapping or read-only shares are
rejected rather than replaced.

Only directories inside configured writable shares are accepted. The writable
container rootfs, image store, records, and logs live on the VM disk; bind mounts
provide live access to shared Mac files. File ownership, executable permissions,
case sensitivity, and filesystem events follow the shared filesystem's behavior.

```sh
casklet machine init --cpus 4 --memory 4 --disk 20
casklet machine status
casklet machine stop
casklet machine start
```

Initialization creates the VM and applies resources; `start` is repeatable.
`status` does not create a VM. Stopping the machine terminates running containers
and preserves their data. After starting it again, use `start` or `restart` to
run retained containers. Engine updates occur automatically when a different
client build is used, without replacing container or image storage.

CPU, memory, and process limits apply inside the VM and are additionally bounded
by VM resources. User namespace mappings and rootless identities refer to Linux
guest accounts. The Mac client does not escalate host privileges. The engine
uses the guest's passwordless sudo and systemd delegation when required.

Published ports use two stages: VM-to-container NAT and Lima forwarding back to
the Mac. TCP and UDP support Mac addresses `127.0.0.1` (local access) and `0.0.0.0`
(all interfaces); other explicit host addresses are currently rejected. Host
port 22 is reserved by Lima. Port availability is checked on the Mac before
launch; forwarding discovery is asynchronous and can take a few seconds. An
external process claiming the port after that check can still prevent
forwarding. Lima startup forces its gRPC forwarder so UDP is supported.
Only the engine's dedicated published-port addresses are forwarded; other VM
services are excluded. `inspect` reports Mac published-port addresses and
canonical Mac filesystem paths in `config`; its `guest_resources` field retains VM paths and forwarding
addresses for diagnostics. The built-in template is shown as `builtin:busybox`.
Inspection does not require the original source directory to still exist.
`--rootfs` and imports must contain Linux executables matching the Mac/VM CPU
architecture; macOS binaries are not container workloads.

## Resource and execution options

```sh
casklet run --rootfs ./rootfs/busybox \
  --memory 128m --pids-limit 64 --cpus 0.5 --timeout 30s \
  --env MODE=demo --workdir /tmp --user 1000:1000 --read-only \
  -- /bin/sh -c 'id; pwd; echo "$MODE"; echo hello > result; cat result'
```

| Option | Behavior |
| --- | --- |
| `--memory` | Positive bytes or binary `k`, `m`, `g` units; default `128m` |
| `--pids-limit` | Aggregate process/thread limit; default `64` |
| `--cpus` | `0` for unlimited, or `0.01–1000` cores with up to three decimals |
| `--timeout` | Command deadline; `0` disables it |
| `--stop-timeout` | Shutdown grace from `0s` to `1m`; default `5s` |
| `--env KEY=VALUE` | Repeatable; last assignment wins, empty values allowed |
| `--workdir` | Absolute directory; image default or `/`; directory templates require it to exist |
| `--user UID[:GID]` | Numeric identity; GID defaults to UID; overrides image User, otherwise `0:0` |
| `--read-only` | Read-only root; `/tmp` stays writable, bind settings are independent |

The default environment is `PATH=/bin:/usr/bin`, `HOME=/`, `LANG=C`. A numeric
non-root user needs access to the configured working directory and executables.
Init, commands, descendants, and managed exec sessions share aggregate limits.

## OCI/Docker images

```sh
casklet run -d --name redis --image redis:8
casklet run -d --name web --network bridge -p 127.0.0.1:8080:80 --image nginx:stable
casklet run -d --name database --memory 512m --env POSTGRES_PASSWORD=example-password \
  --image postgres:17
```

`--image` accepts a registry name, `NAME@sha256:DIGEST`, or a full local image ID.
Docker Hub, its `library/` namespace, and the `latest` tag are defaults. An uncached
name is pulled automatically inside the VM, selecting `linux/arm64` on Apple
silicon or `linux/amd64` on Intel. Anonymous-access registries are supported;
private-registry login and arbitrary insecure HTTP registries are not provided.
Loopback HTTP registries are supported for local development and tests.

The image store verifies downloaded blobs and uncompressed layer DiffIDs, applies
layers in order (including whiteouts and opaque directories), preserves numeric
ownership, and atomically publishes an independent rootfs. Programs may use dynamic
libraries and merged `/usr` layouts; static BusyBox is only a requirement for the
managed builtin template. Extraction strips setuid/setgid bits and does not apply
file capabilities or extended attributes. Device nodes, sockets, and FIFOs in layers
are rejected. Pulls are bounded to 15 minutes, 256 layers, 1 million entries per
layer, 4 GiB uncompressed per layer, and 16 GiB uncompressed in total, and require
enough VM disk space for staging plus the unpacked tree.

Image `Entrypoint + Cmd`, `Env`, `WorkingDir`, `User`, and `StopSignal` supply startup defaults.
Arguments after `--` replace `Cmd` while retaining `Entrypoint`. `--entrypoint PATH`
replaces the entrypoint and clears default `Cmd`; `--entrypoint ''` clears both.
`--env`, `--workdir`, and numeric `--user UID[:GID]` override image values. Image
user/group names are resolved against its `/etc/passwd` and `/etc/group`; an omitted
group uses the account's primary group, or GID 0 for a numeric UID without an account.
Supplementary groups are not inherited. A missing working directory is created in
the private root before execution. Image `EXPOSE` and `VOLUME` metadata do not publish
ports or provision storage; provide `--network bridge -p ...` and `--mount` explicitly.

```sh
casklet image pull redis:8 # Refresh the cached tag explicitly.
casklet image ls --json
casklet run --image redis:8 -- --version
casklet run --image redis:8 --entrypoint '' -- /bin/sh -c 'id; pwd'
```

Both `run --image` and `image pull` report per-layer download, verification,
extraction, and completion to stderr. Download byte counts use compressed registry
layer sizes. A terminal updates progress bars in place for up to six recent layers;
redirected output uses throttled text lines without terminal controls. `auto` selects
the display from the Mac's stderr, including over the VM SSH connection. Cached runs
show `Using cached image`; an unchanged explicit pull shows `Image is up to date`
after verification. Failed or canceled layers never report pull completion.

```sh
casklet image pull --progress=plain redis:8
casklet run -d --name redis --progress=auto --image redis:8
image_id=$(casklet image pull redis:8 2>pull.log)
```

Use `--progress=tty` to force dynamic bars or `--progress=plain` for plain text.
The image ID, container ID, and foreground application output stay on stdout.

Cached names run without contacting their registry. A mutable tag keeps its cached
version until `image pull` refreshes it; use a digest reference for a fixed source.
Local `sha256:` IDs hash the unpacked content, ownership, architecture, and startup
configuration and differ from registry manifest digests (`manifest_digest` in image
JSON). A retained container records its immutable local ID and merged configuration;
start/restart preserves its writes and does not re-resolve its original tag.

### Directory imports

```sh
image_id=$(casklet image import ./rootfs/busybox)
casklet run --image "$image_id" -- /bin/echo hello
casklet image rm "$image_id"
```

Directory imports have no default command. Imports copy a stable Linux filesystem;
later source changes do not alter it. Running copies are independent. Omitting
`--image` and `--rootfs` selects builtin BusyBox. Deletion requires a full local ID
and is blocked while the image is leased or referenced by a retained container.
Remove referencing containers first; deleting a cached target makes its name a
cache miss on the next run.

## Persistent data

```sh
mkdir -p "$HOME/casklet-data"
casklet run --rootfs ./rootfs/busybox \
  --mount "type=bind,source=$HOME/casklet-data,target=/data" \
  -- /bin/sh -c 'echo hello > /data/message'
```

Bind an existing host directory using
`type=bind,source=/HOST,target=/PATH[,readonly]`; repeat for up to 32 mounts.
Sources must be clean absolute directories without symlink components, outside
protected host paths and the template. Targets must not overlap or replace
`/proc`, `/dev`, `/sys`, or `/tmp`. Mounted descendants of a source are excluded.
Host data survives container removal. Set source permissions for the workload's
host identity; bind ownership is not remapped.

### Named volumes

Named volumes live at `/var/lib/casklet/volumes` on the VM disk, outside container
roots. Create them explicitly before mounting. New volumes are empty and owned by
root; image files are not copied into the volume. Applications running as another
UID/GID need matching permissions, which can be initialized by a trusted root
container. Named volumes currently require execution without user namespaces.

```sh
casklet volume create app-data
casklet run --mount type=volume,source=app-data,target=/data -- /bin/sh -c 'echo saved > /data/message'
casklet run --mount type=volume,source=app-data,target=/data,readonly -- /bin/cat /data/message
casklet volume ls --json
casklet volume inspect app-data
casklet volume rm app-data
```

Data survives `stop`, `restart`, and container removal. `volume rm` deletes the
data and refuses active foreground leases or references from any retained
container, including stopped ones. Remove those containers first. Volumes are
never translated to Mac paths and do not require a host filesystem share.

## Terminals and exec

```sh
casklet run -it --rootfs ./rootfs/busybox -- /bin/sh
casklet run -d --name worker --rootfs ./rootfs/busybox -- /bin/sleep 300
casklet exec -it worker -- /bin/sh
casklet exec --env MODE=check --workdir /tmp --timeout 10s \
  worker -- /bin/sh -c 'pwd; echo "$MODE"'
printf 'hello\n' | casklet exec -i worker -- /bin/cat
```

`-t` allocates a terminal and merges stdout/stderr; add `-i` for input. `-it`
requires terminal stdin. Foreground runs without `-t` forward stdin by default;
exec does not. Detached runs reject terminal/input options. The client forwards
external termination signals, and a separate watchdog cleans up foreground
sessions if the Mac client exits abruptly. Detached containers remain independent
of their launching client.

Exec requires a running detached container and kernel support for `clone3` with
`CLONE_INTO_CGROUP`. It shares the container namespaces, filesystem, identity,
security policy, and resource limits. Only environment and working directory
can be overridden. Output goes to the caller; it is not added to container logs.
Exiting an exec shell leaves the main container running. Shutdown cancels all
exec sessions; client disconnect and timeout clean up that session's descendants.

## Background lifecycle and diagnostics

```sh
casklet ps -a --json
casklet logs --tail 20 worker
casklet inspect worker
casklet stats --json --interval 250ms worker
casklet stop --timeout 2s worker
casklet start worker
casklet restart worker
casklet stop worker
casklet wait worker
casklet rm worker
```

`ps` lists active containers; `-a` includes completed ones. `stop` sends the configured stop signal
and forces shutdown after the grace period. `wait` prints and returns the exit
code for the execution it observed. `start` and `restart` keep the private rootfs
but recreate transient namespaces, cgroups, and temporary storage. `rm` requires
a stopped container and removes its private files, metadata, and logs.

`start` and `restart` check saved published ports on the Mac before creating a
new execution. They briefly wait for Lima to release old forwarding sockets.
If a port remains occupied, the command returns 125 and keeps the stopped
generation and its files. A failed `restart` can therefore leave the container
stopped; release the port and retry `start` or `restart`. `start` on an already
running container remains idempotent.

The default managed stop signal is the image's `StopSignal`, or SIGTERM if absent.
Use `run --stop-signal SIGQUIT` (or a Linux number from 1 to 64) to override it.
`stop`, `restart`, and systemd VM shutdown use the saved signal and stop timeout,
then force SIGKILL when the deadline expires. Signal names/numbers always have
Linux meanings, including when entered on macOS. The supervisor and init receive
their normal control signals; only the workload receives the selected stop signal.
External signals on foreground runs remain unchanged; timeout, orphan cleanup,
and descendant cleanup retain their existing SIGTERM/SIGKILL behavior.

```sh
casklet run -d --name signal-worker --stop-signal SIGUSR1 --stop-timeout 2s -- /bin/sh -c "trap 'exit 0' USR1; while :; do sleep 1; done"
casklet stop signal-worker
```

Detached stdout/stderr share rotating logs, defaulting to four files of 4 MiB.
Set `--log-max-size` and `--log-max-files` on detached `run`; retained logs are
limited to 64 MiB total. `logs -f` follows output; `--tail` accepts 0–1000000.
If log storage is temporarily busy beyond its bounded lock wait, the affected
chunk is discarded, `log_truncated` is set, and capture resumes for later output.
The completion error reports that loss. Logs and metadata for other containers
can continue during rotation or deletion.
`inspect` reports environment names without values. `stats` returns one sample;
CPU 100% means one busy core, and unavailable metrics are `N/A` or JSON `null`.

Command exit codes pass through. Signals return `128 + signal`, command timeout
returns `124`, and configuration/startup errors return `125`. Diagnostics go to
stderr. Argument errors link to the relevant command help. Lifecycle errors
include recovery commands, such as stopping a running container before removal
or listing containers after a failed lookup. Successful foreground `run` and
`exec` commands return 125 if cleanup fails. A command's nonzero exit code is
preserved, with cleanup diagnostics on stderr.
For detached containers, `inspect` reports cleanup failures separately in the
`cleanup_failures` array; `previous_exit` retains them after a restart. The array
contains failure stages rather than private error messages. `wait` still returns
the command's exit code. Resources that could not be safely cleaned remain
available for recovery. A foreground command that exits zero but encounters an
infrastructure error returns 125 from the CLI.

Interrupted container creation and removal leave private transaction directories.
Later management operations reclaim verified abandoned transactions automatically.
Directories with live leases, unsafe ownership or permissions, or mounted
subtrees are preserved; unsafe artifacts produce a diagnostic.

## IPv4 networking

```sh
casklet run -d --name web --rootfs ./rootfs/busybox \
  --network bridge --dns 8.8.8.8 -p 127.0.0.1:8080:80 \
  -- /bin/httpd -f -p 80 -h /tmp
```

Default `--network none` provides loopback only. `bridge` adds a veth pair,
shared bridge, IPv4 masquerading, and DNS configuration inside the VM. The guest
engine requires root, `iproute2`, `nftables`, and `conntrack`, configured automatically. Repeat `--dns` for up to three non-loopback
IPv4 servers; otherwise upstream IPv4 resolvers are discovered on the host.

Publish up to 32 mappings with
`-p [HOST_IP:]HOST_PORT:CONTAINER_PORT[/tcp|udp]`. Defaults are host address
`0.0.0.0` and TCP; specify `127.0.0.1` for host-only access. Networking is recreated
on restart and cleaned up after exit. There is no IPv6 or host-network mode.

## Disk usage and cache cleanup

```sh
casklet system df
casklet system df --json
casklet image prune --dry-run
casklet image prune
```

`system df` reports guest filesystem capacity/free/available bytes and allocated
blocks for images, retained containers (including logs), named volumes, transient
runs, and templates. Hardlinks are counted once within a category, sparse files
use allocated blocks, symlinks are not followed, and mounted descendants are
excluded. Live workloads make this a sampled report rather than an atomic snapshot.
Host bind data and the Mac's sparse VM disk file are outside these totals. A
low-space notice appears below 1 GiB or 10% available; JSON exposes `low_space`.

`image prune --dry-run` prints eligible IDs. Without `--dry-run`, the command
removes unused cached images, including tagged images, and prints each removed ID.
Active leases and references from all retained containers prevent deletion.
Each candidate is rechecked immediately before removal. Cancellation or another
error can leave a partial prune; printed IDs identify completed removals.
Containers, logs, named volumes, and host bind data are preserved. Removing files
frees guest filesystem space but does not promise immediate Mac disk compaction.

## Security and rootless execution

Workloads use reduced capabilities and `no_new_privs`. Default seccomp filters
dangerous syscalls across all threads and descendants; unsupported kernels fail
startup. Directory templates and non-root image users receive no capabilities.
OCI images starting as root retain only `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETUID`,
`SETGID`, `SETPCAP`, `KILL`, and `NET_BIND_SERVICE` for data initialization, privilege dropping,
and HTTP listeners. These remain bounded; namespace/mount/network administration
is unavailable. `/dev/shm` is a private 64 MiB tmpfs charged to the container memory
limit. OCI images currently do not support user namespaces or rootless execution.
It is a denylist, so use trusted workloads. `--seccomp unconfined`
disables the filter when a trusted command needs blocked syscalls.

For foreground user namespaces, provide nonoverlapping UID/GID mappings that
cover container root and the selected command identity:

```sh
casklet run --rootfs ./rootfs/busybox --userns \
  --uid-map 0:200000:2000 --gid-map 0:300000:2000 --user 1000:1000 \
  -- /bin/sh -c 'id; cat /proc/self/uid_map'
```

Choose host ranges reserved for the runtime. Copied rootfs ownership is shifted
to mapped root without following symlinks; bind ownership stays unchanged.
User namespaces support foreground execution and loopback networking only.

For rootless execution, run the macOS client as your regular user:

```sh
casklet run --rootless --rootfs ./rootfs/busybox -- /bin/sh -c 'id; hostname'
```

Rootless mode maps container `0:0` to the VM login user's UID/GID and uses a
delegated systemd user scope without guest sudo. It requires a directory
rootfs and supports foreground execution with loopback-only networking.
The product VM automatically configures the private user runtime directory,
cgroup delegation, and AppArmor user namespace permission for the guest engine.
Explicit UID/GID mapping ranges refer to guest Linux identities, not Mac accounts.
