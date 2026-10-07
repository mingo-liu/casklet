# Usage guide

[Quick start](../README.md) · [Development](development.md) · [Architecture](../ARCHITECTURE.md)

Run `mdocker help` for the complete option reference. Management flags precede
the container ID or exact name; `run` and `exec` require `--` before the command.
Host environment variables are never inherited by workloads.

## macOS execution environment

Run every public command on the Mac as your regular user. The client creates or
starts the `mini-docker-runtime` Lima VM as needed and installs its bundled
mini-docker Linux engine. The default rootfs is the VM's static BusyBox template:

```sh
mdocker run -- /bin/sh
mdocker doctor
mdocker rootfs ./rootfs/busybox
```

An explicit `--rootfs DIRECTORY`, `image import DIRECTORY`, or bind source refers
to a Mac directory. Relative template/import paths resolve against the Mac's
current directory. Host symlinks are resolved before translating a path into its
shared guest location. Workload command arguments, environment values, and
container paths are passed unchanged; shell expansion happens only when you
explicitly run a shell in the container.

The home directory is shared writable with the VM. Additional directories can
be configured before first use with `mdocker machine init --mount DIRECTORY`.
To add a directory to an existing VM, stop it, add the share, and start it:

```sh
mdocker machine stop
mdocker machine share /Volumes/Projects
mdocker machine start
```

Stopping the VM terminates running containers and preserves their files. Restart
retained containers with `mdocker start NAME`. Adding a share preserves the VM
disk, resources, forwarding rules, and existing shares. Sharing an already
covered writable directory is a no-op. Overlapping or read-only shares are
rejected rather than replaced.

Only directories inside configured writable shares are accepted. The writable
container rootfs, image store, records, and logs live on the VM disk; bind mounts
provide live access to shared Mac files. File ownership, executable permissions,
case sensitivity, and filesystem events follow the shared filesystem's behavior.

```sh
mdocker machine init --cpus 4 --memory 4 --disk 20
mdocker machine status
mdocker machine stop
mdocker machine start
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
services are excluded. `inspect` currently reports guest filesystem and network
addresses. `--rootfs` and imports must contain Linux executables matching the
Mac/VM CPU architecture; macOS binaries are not container workloads.

## Resource and execution options

```sh
mdocker run --rootfs ./rootfs/busybox \
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
| `--workdir` | Existing absolute directory inside the container; default `/` |
| `--user UID[:GID]` | Numeric identity; GID defaults to UID, default `0:0` |
| `--read-only` | Read-only root; `/tmp` stays writable, bind settings are independent |

The default environment is `PATH=/bin:/usr/bin`, `HOME=/`, `LANG=C`. A numeric
non-root user needs access to the configured working directory and executables.
Init, commands, descendants, and managed exec sessions share aggregate limits.

## Local images

```sh
image_id=$(mdocker image import ./rootfs/busybox)
mdocker image ls --json
mdocker run --image "$image_id" -- /bin/echo hello
mdocker image rm "$image_id"
```

Images are immutable, content-addressed local snapshots. Use the full
`sha256:` ID with `--image`. Omitting both source options uses the built-in BusyBox template. Imports copy the source;
later source changes do not alter the image. Running copies are independent.
Deletion is blocked while an image is leased or referenced by a retained
container. Remove those containers first. There is no registry or layer support.

## Persistent data

```sh
mkdir -p "$HOME/mini-docker-data"
mdocker run --rootfs ./rootfs/busybox \
  --mount "type=bind,source=$HOME/mini-docker-data,target=/data" \
  -- /bin/sh -c 'echo hello > /data/message'
```

Bind an existing host directory using
`type=bind,source=/HOST,target=/PATH[,readonly]`; repeat for up to 32 mounts.
Sources must be clean absolute directories without symlink components, outside
protected host paths and the template. Targets must not overlap or replace
`/proc`, `/dev`, `/sys`, or `/tmp`. Mounted descendants of a source are excluded.
Host data survives container removal. Set source permissions for the workload's
host identity; bind ownership is not remapped.

## Terminals and exec

```sh
mdocker run -it --rootfs ./rootfs/busybox -- /bin/sh
mdocker run -d --name worker --rootfs ./rootfs/busybox -- /bin/sleep 300
mdocker exec -it worker -- /bin/sh
mdocker exec --env MODE=check --workdir /tmp --timeout 10s \
  worker -- /bin/sh -c 'pwd; echo "$MODE"'
printf 'hello\n' | mdocker exec -i worker -- /bin/cat
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
mdocker ps -a --json
mdocker logs --tail 20 worker
mdocker inspect worker
mdocker stats --json --interval 250ms worker
mdocker stop --timeout 2s worker
mdocker start worker
mdocker restart worker
mdocker stop worker
mdocker wait worker
mdocker rm worker
```

`ps` lists active containers; `-a` includes completed ones. `stop` sends SIGTERM
and forces shutdown after the grace period. `wait` prints and returns the exit
code for the execution it observed. `start` and `restart` keep the private rootfs
but recreate transient namespaces, cgroups, and temporary storage. `rm` requires
a stopped container and removes its private files, metadata, and logs.

Detached stdout/stderr share rotating logs, defaulting to four files of 4 MiB.
Set `--log-max-size` and `--log-max-files` on detached `run`; retained logs are
limited to 64 MiB total. `logs -f` follows output; `--tail` accepts 0–1000000.
`inspect` reports environment names without values. `stats` returns one sample;
CPU 100% means one busy core, and unavailable metrics are `N/A` or JSON `null`.

Command exit codes pass through. Signals return `128 + signal`, command timeout
returns `124`, and configuration/startup errors return `125`. Diagnostics go to
stderr. A completed command keeps its exit code during descendant cleanup.

## IPv4 networking

```sh
mdocker run -d --name web --rootfs ./rootfs/busybox \
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

## Security and rootless execution

Workloads use reduced capabilities and `no_new_privs`. Default seccomp filters
dangerous syscalls across all threads and descendants; unsupported kernels fail
startup. It is a denylist, so use trusted workloads. `--seccomp unconfined`
disables the filter when a trusted command needs blocked syscalls.

For foreground user namespaces, provide nonoverlapping UID/GID mappings that
cover container root and the selected command identity:

```sh
mdocker run --rootfs ./rootfs/busybox --userns \
  --uid-map 0:200000:2000 --gid-map 0:300000:2000 --user 1000:1000 \
  -- /bin/sh -c 'id; cat /proc/self/uid_map'
```

Choose host ranges reserved for the runtime. Copied rootfs ownership is shifted
to mapped root without following symlinks; bind ownership stays unchanged.
User namespaces support foreground execution and loopback networking only.

For rootless execution, run the macOS client as your regular user:

```sh
mdocker run --rootless --rootfs ./rootfs/busybox -- /bin/sh -c 'id; hostname'
```

Rootless mode maps container `0:0` to the VM login user's UID/GID and uses a
delegated systemd user scope without guest sudo. It requires a directory
rootfs and supports foreground execution with loopback-only networking.
The product VM automatically configures the private user runtime directory,
cgroup delegation, and AppArmor user namespace permission for the guest engine.
Explicit UID/GID mapping ranges refer to guest Linux identities, not Mac accounts.
