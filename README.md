# mini-docker

A small Go container runtime for Linux arm64 and amd64. Run foreground or background commands in separate PID, mount, UTS, IPC, and network namespaces, with a copied BusyBox root filesystem and cgroups v2 memory, process, and optional CPU limits.

Foreground execution, interactive terminals, background container management, interactive container execution, inspection, resource statistics, directory bind mounts, local images, external IPv4 networking, and lifecycle extensions are implemented and validated in the dedicated Linux VM. Execution options include configurable environments, working directories, numeric users, read-only root filesystems, and content-addressed local images.

## Requirements

Use a dedicated Ubuntu 24.04 development VM with systemd, Linux 6.8 or newer, cgroups v2, root privileges, Go 1.25 or newer, and `busybox-static`. Required cgroup interfaces include `memory`, `pids`, `memory.swap.max`, and `cgroup.kill`. Foreground execution uses a delegated systemd scope; detached execution creates its own delegated service. Background management requires `/usr/bin/systemd-run` and `/usr/bin/systemctl`.

Use this runtime for trusted programs. Commands run as container UID 0 by default, with reduced capabilities; `--user` selects another numeric identity. It does not provide a security guarantee for untrusted code. There are no image registries or managed volumes. Rootless execution is available for foreground, loopback-only directory-based runs.

## Development VM on macOS

Install Lima, then start the dedicated VM:

```sh
brew install lima
limactl start --name mini-docker dev/lima.yaml
```

The configuration installs BusyBox, build tools, and checksum-verified Go 1.27.1 from the [official Go downloads](https://go.dev/dl/). [Lima plain mode](https://lima-vm.io/docs/config/plain/) disables host filesystem sharing; transfer a source snapshot into a writable VM-local directory. Run this from the repository root:

```sh
set -o pipefail
./scripts/snapshot.sh | limactl shell mini-docker sh -c \
  'source_dir=$(mktemp -d "$HOME/mini-docker-source.XXXXXXXX") &&
    tar -xzf - -C "$source_dir" && printf "Source directory: %s\n" "$source_dir"'
limactl shell mini-docker
# Replace the suffix with the exact directory printed by the transfer.
cd ~/mini-docker-source.XXXXXXXX
make build
make rootfs
sudo make install
```

Repeat the transfer after source changes and use the new printed directory. The snapshot script includes existing tracked and unignored files using null-delimited filenames. Each transfer uses a fresh directory, so deleted source files cannot survive in subsequent builds. Previous snapshots remain available until explicitly removed. The VM keeps build artifacts, rootfs templates, and runtime state on its own filesystem. No host directory is mounted into the VM. On an existing Ubuntu VM, install `busybox-static binutils make iproute2 nftables util-linux conntrack` with `apt-get`, and install Go 1.25 or newer before using the commands below.

## Run

```sh
mdocker doctor --rootfs ./rootfs/busybox
mdocker run --rootfs ./rootfs/busybox \
  --hostname mini --memory 128m --pids-limit 64 --timeout 30s \
  -- /bin/sh -c 'hostname; ps; echo hello'
printf 'hello\n' | mdocker run \
  --rootfs ./rootfs/busybox -- /bin/cat
```

`make build` produces `bin/mdocker`; after building, `sudo make install` installs it as `/usr/local/bin/mdocker` inside the Linux VM. Run `mdocker` from any directory on that host, or use `./bin/mdocker` directly without installing. `make install PREFIX=/your/prefix` selects another installation prefix; ensure its `bin` directory is on `PATH`.

The CLI automatically uses `sudo` when root privileges are needed. `run --rootless` keeps the caller identity and uses `systemd-run --user --scope`; it never invokes sudo. Foreground runs and `doctor` create a delegated `systemd-run --scope` when the current process is not already in a suitable scope. Detached runs create their own delegated services; management and image commands do not create foreground scopes. Standard input, terminal handling, and command exit status pass through. Help and argument errors do not invoke `sudo`. This is a Linux CLI; on macOS, enter the VM with `limactl shell mini-docker` first.

Arguments after the required `--` are executed directly; explicitly invoke `/bin/sh -c` for shell syntax. The old `scripts/run-linux.sh` remains a compatibility launcher using `sudo systemd-run --scope` and accepts `MINI_DOCKER_BINARY` to select another executable; `bin/mini-docker` is a compatibility symlink to `mdocker`.

Defaults: hostname `mini`, memory `128m`, process/thread limit `64`, timeout `0` (unlimited), shutdown grace `5s`. Memory suffixes `k`, `m`, and `g` use powers of 1024. Each new container copies the rootfs template and mounts independent `/proc` and temporary storage. Foreground execution removes its working filesystem after exit; detached containers retain their private copy until `rm`. Explicit directory bind mounts preserve data in their host sources. Networking defaults to loopback only; `--network bridge` enables external IPv4 connectivity. By default, the command receives a fixed environment and starts in `/`.

### Resource and execution options

```sh
mdocker run --rootfs ./rootfs/busybox \
  --cpus 0.5 --env MODE=demo --env EMPTY= --workdir /tmp \
  --user 1000:1000 --read-only \
  -- /bin/sh -c 'id; pwd; echo "$MODE"; echo hello > result; cat result'
```

| Option | Behavior |
| --- | --- |
| `--cpus` | `0` means unlimited; accept `0.01` through `1000`, with up to three decimal places. Set total container CPU bandwidth over a 100ms period; `0.5` allows half of one CPU. Require the delegated CPU controller only when a limit is requested. |
| `--env KEY=VALUE` | Repeat to add variables; the last assignment wins. Accept empty values and `=` in values. Names use letters, digits, and underscores and cannot start with a digit. Override the fixed `PATH=/bin:/usr/bin`, `HOME=/`, and `LANG=C` defaults without inheriting host variables. |
| `--workdir /PATH` | Use an existing directory inside the container; default `/`. Missing or inaccessible directories fail startup. Command lookup uses the configured `PATH` and working directory inside the container. |
| `--user UID[:GID]` | Use numeric IDs from `0` through `4294967294`; GID defaults to UID. Clear all capability sets and, except in rootless mode, supplementary groups before starting the command. The container init uses the same identity to supervise descendants. |
| `--seccomp PROFILE` | Default `default` filters dangerous syscalls on every thread and descendant. `unconfined` disables only seccomp; capability reduction and `no_new_privs` remain enforced. |
| `--userns` | Foreground user namespace; supply repeatable `--uid-map` and `--gid-map` ranges. Requires `--network none`. |
| `--uid-map C:H:N`, `--gid-map C:H:N` | Map `N` container IDs starting at `C` to host IDs starting at `H`. Root and the selected command identity must be mapped; ranges cannot overlap. |
| `--rootless` | Foreground run without host privileges. Implies a user namespace with container `0:0` mapped to the caller UID/GID; requires a directory rootfs and loopback networking. |
| `--read-only` | Mount the copied root read-only. `/tmp` remains a writable, size-limited tmpfs; minimal `/dev` remains usable. Bind mounts retain their own read-only setting. The default root is writable; detached containers retain it until removal. |
| `--network MODE` | Default `none` supplies loopback only. `bridge` adds IPv4 connectivity through a veth pair, a shared bridge, and NAT. |
| `--dns IPV4` | Repeat for up to three non-loopback IPv4 resolvers; requires `--network bridge`. Otherwise discover upstream IPv4 resolvers on the Linux host. |
| `-p, --publish [HOST_IP:]HOST_PORT:CONTAINER_PORT[/tcp\|udp]` | Repeat for up to 32 mappings; requires `--network bridge`. Default host address is `0.0.0.0`, protocol `tcp`. |
| `--stop-timeout DURATION` | Grace before forcing shutdown after a signal, timeout, or main-process exit with remaining descendants. Default `5s`; accept `0s` through `1m`. Zero skips the grace period. |
| `--mount type=bind,source=/HOST,target=/PATH[,readonly]` | Bind an existing host directory into the container. Repeat for up to 32 independent targets; add `readonly` to prevent container writes. |

The `--user` flag selects the container identity; `--userns` and `--rootless` control host mappings. Copied files belong to container root (mapped host root when enabled); custom rootfs templates must grant the selected user access to its working directory and executables. `make rootfs` now generates a traversable root directory; regenerate an older template if its root mode is `0700`.

CPU quotas use cgroups v2 [`cpu.max`](https://www.kernel.org/doc/html/v6.8/admin-guide/cgroup-v2.html#cpu). The supervisor stays outside the workload quota; init, command processes, and their threads share the limit.

Normal command exit codes pass through. Signal exits use `128 + signal`; timeout returns `124` while the main command is running; completed commands keep their exit code during descendant cleanup; configuration, unsupported-platform, and startup errors return `125`. Diagnostics go to stderr. SIGINT and SIGTERM are forwarded, with a bounded shutdown grace period.

## Security capabilities

The default [seccomp BPF](https://www.kernel.org/doc/html/latest/userspace-api/seccomp_filter.html) profile applies after filesystem/network/terminal setup and privilege reduction, before workload startup. Linux TSYNC synchronizes existing Go threads; fork/exec descendants inherit it. Background runs, restart, and managed `exec` use the saved profile, and `inspect.config.seccomp` reports it. Existing configurations without the field receive the default profile when started again.

The profile returns `EPERM` for namespace creation/entry, mounting (including the newer mount API), ptrace/process-memory access, BPF, perf events, kernel keyrings, userfaultfd, io_uring setup, module loading, kexec, reboot, swap, quota changes, and terminal input injection. Ordinary thread/process creation is allowed; clone namespace flags are blocked. `clone3` returns `ENOSYS` for libc fallback because its pointer arguments cannot be inspected safely. Foreign syscall ABIs, including x32 on amd64, terminate the process with `SIGSYS`. Unlisted syscalls remain allowed: this is a denylist, not a complete security boundary. Unsupported kernels fail startup instead of silently dropping protection. Use `--seccomp unconfined` only when a trusted workload needs a blocked syscall.

Use explicit [user namespace mappings](https://man7.org/linux/man-pages/man7/user_namespaces.7.html) for a privileged foreground run:

```sh
mdocker run --rootfs ./rootfs/busybox --userns \
  --uid-map 0:200000:1000 --uid-map 1000:400000:1000 \
  --gid-map 0:300000:2000 --user 1000:1234 \
  -- /bin/sh -c 'id; cat /proc/self/uid_map; cat /proc/self/gid_map'
```

Each UID and GID mapping contains `CONTAINER_ID:HOST_ID:SIZE`, with at most 340 nonoverlapping ranges per type. IDs end at `4294967294`; zero-length or overflowing ranges and unmapped command IDs are rejected. Choose host ranges reserved for this runtime: assigning an existing host user's ID grants that user's ordinary filesystem access. Template contents are copied as container root; source ownership is not preserved. The private copy is shifted to the mapped host root without following symlinks. Bind source ownership remains unchanged; files created through writable binds use mapped host IDs. Grant access to the mapped host identity before mounting a source. Numeric non-root users still need appropriate permissions inside the template.

User namespaces currently support foreground runs, directory templates or local images, read-only roots, bind mounts, resource limits, signals, timeouts, and terminals. Detached management and bridge networking are rejected. Foreground commands have no managed `exec` endpoint. Mapped privileged runs use a root-owned coordination directory at `/tmp/mini-docker-userns/`, with private run directories owned by the mapped root and root-owned lock/receipt files. Ordinary runs keep `/var/lib/mini-docker/runs/`; rootless runs use `/run/user/UID/mini-docker/runs/`. Normal completion removes private copies; subsequent runs in the same mode recover verified abandoned directories after workloads have exited. Keep host ranges and runtime directories under administrator control.

For rootless execution, start from a real Linux login session with a running systemd user manager and a private `/run/user/UID`. The cgroups v2 user subtree must delegate `memory`, `pids`, and, for CPU quotas, `cpu`; it must expose writable `memory.swap.max`, `memory.oom.group`, and `cgroup.kill`. Every invocation creates a fresh user scope and enforces the same limits as privileged execution. Missing controllers, denied user namespaces, and inaccessible controls fail with `125`; there is no fallback to sudo or unlimited resources. The host administrator may need to configure controller delegation for `user@.service` on systems with restricted defaults.

```sh
# Run as your regular Linux login user, without sudo.
mdocker run --rootless --rootfs ./rootfs/busybox --cpus 0.25 \
  --memory 64m --pids-limit 32 --read-only \
  -- /bin/sh -c 'id; cat /proc/self/uid_map; echo writable > /tmp/result'
```

Rootless mode maps only the caller's single UID/GID to container `0:0`. Other `--user` IDs, custom ranges, stored images, detached management, bridge networking, DNS, and publishing are unsupported. Directory binds are supported within the caller's existing access. No subordinate-ID helpers, registry integration, or rootless network proxy is required or provided. Linux denies `setgroups` for this unprivileged mapping, so the caller's existing supplementary host groups remain; unmapped groups can appear as overflow IDs inside the container. All capability sets are still cleared and `no_new_privs` is enforced. Device files are fixed binds of host null/zero/random/urandom/tty devices; temporary storage and devpts remain private and bounded.

Ubuntu 24.04 and other AppArmor hosts may restrict unprivileged user namespace creation. Install the scoped profile from this repository for `/usr/local/bin/mdocker`, leaving the host's global user namespace policy in place:

```sh
sudo apt-get install -y apparmor-utils
sudo install -m 0644 dev/apparmor/mini-docker /etc/apparmor.d/mini-docker
sudo apparmor_parser -r /etc/apparmor.d/mini-docker
# For an uninstalled development binary, explicitly select the loaded profile:
aa-exec -p mini-docker-rootless -- ./bin/mdocker run --rootless \
  --rootfs ./rootfs/busybox -- /bin/id
```

The profile permits user namespace creation; it does not bypass kernel ID-mapping rules or grant host capabilities. Customize its executable attachment when installing to another prefix. On hosts without AppArmor user namespace restrictions, this profile is unnecessary. Integration tests use the existing logged-in account from `SUDO_UID`, or `MINI_DOCKER_ROOTLESS_UID` when invoking the suite directly as root; that account needs a running user manager. Load the development profile first when the host requires it. Run `./scripts/test-linux.sh -test.run TestSecurity` for the focused security suite.

## External networking

Networking remains opt-in. On the Linux host, install the networking tools if the VM predates this feature:

```sh
sudo apt-get install -y iproute2 nftables util-linux conntrack
mdocker run --rootfs ./rootfs/busybox --network bridge \
  -- /bin/sh -c 'ip addr show eth0; ip route; cat /etc/resolv.conf'
mdocker run --rootfs ./rootfs/busybox --network bridge \
  --dns 1.1.1.1 --dns 8.8.8.8 -- /bin/nslookup example.com
```

Bridge containers receive unique addresses from `10.231.0.2` through `10.231.0.254`, an `eth0` veth endpoint, and a default route through the shared `mdocker0` bridge at `10.231.0.1/24`. Outbound traffic uses IPv4 masquerading. Concurrent containers share this bridge and can communicate directly; it is not a network policy boundary. The subnet is fixed in this version. Startup rejects overlapping host routes, an unrelated `mdocker0` interface, and occupied runtime interface or NAT table names.

DNS is written into the private filesystem's `/etc/resolv.conf` before a read-only root is installed. Explicit `--dns` values take precedence. Otherwise the runtime selects up to three non-loopback IPv4 nameservers from `/run/systemd/resolve/resolv.conf`, then `/etc/resolv.conf`; this skips systemd's unreachable container-local stub at `127.0.0.53`. If neither file supplies usable servers, startup fails with instructions to specify `--dns`. Search domains and host resolver options are not inherited. DNS is regenerated on each start, and `exec` uses the installed file. Bridge networking reserves `/etc/resolv.conf` and its staging name `/etc/.mini-docker-resolv`; directory binds covering the resolver, including `/etc`, are rejected.

Publish a BusyBox HTTP server on the Linux host's loopback interface:

```sh
mdocker run -d --name web --rootfs ./rootfs/busybox \
  --network bridge -p 127.0.0.1:8080:8080 \
  -- /bin/sh -c 'mkdir -p /www; echo hello > /www/index.html; httpd -f -p 8080 -h /www'
curl http://127.0.0.1:8080/
mdocker exec web -- /bin/ip addr show eth0
mdocker inspect web
mdocker restart web
mdocker stop web
mdocker rm web
```

The container service must listen on `eth0` or `0.0.0.0`, rather than only its own loopback address. Container commands retain their existing capability restrictions; use unprivileged listening ports such as `8080`. Mapping syntax is `[HOST_IP:]HOST_PORT:CONTAINER_PORT[/tcp|udp]`: `-p 8080:8080` publishes TCP on all host IPv4 addresses, while `-p 127.0.0.1:5353:7777/udp` publishes UDP locally. Ports must be between 1 and 65535; ranges, automatic ports, IPv6 addresses, and other protocols are unsupported. Explicit host addresses must be locally bindable. Duplicate and wildcard-overlapping mappings are rejected. TCP and UDP may use the same host port independently.

Published ports use [nftables destination NAT](https://wiki.nftables.org/wiki-nftables/index.php/Performing_Network_Address_Translation_(NAT)) for incoming and host-originated traffic, including host loopback and container hairpin access. The supervisor also reserves each host socket for the execution's lifetime, so conflicts with host services or other mini-docker containers fail startup. These reservations do not proxy traffic. Mapped connections are source-NATed to the bridge gateway, so container services see that gateway as their peer. Stop releases mappings; start and restart reinstall the saved configuration and fail if a port has since been occupied. Container IP addresses are allocated per execution and are not promised to remain the same. `inspect.config` exposes `network`, explicitly configured `dns`, and `publish`; automatically discovered resolvers can be read through `exec`.

Use the dedicated VM with forwarding permitted by its host firewall. The runtime creates only its own per-execution nftables tables and preserves unrelated firewall rules; an existing firewall drop policy can still block forwarding. IPv4 forwarding is enabled while bridge allocations exist, with its prior on/off setting recorded and restored after the last allocation is removed. Loopback routing is enabled only on the owned bridge. Do not concurrently modify runtime-owned interfaces, firewall tables, or forwarding configuration. With Lima, published host ports belong to the Linux VM; macOS access depends on Lima's port forwarding configuration.

Normal exit, stop, timeout, and startup failure remove veth pairs, owned NAT tables, matching connection-tracking entries, and host neighbor cache entries before releasing the allocation. The last allocation also removes the bridge. A private journal records ownership and reservations before per-container mutations; failed cleanup preserves it for recovery rather than silently reusing resources. Boot identities prevent previous-boot journals from modifying newly configured host resources. Abandoned supervisors are reclaimed through existing `ps`, `stop`, `rm`, and subsequent-run recovery. Retained stopped containers keep their networking configuration, but consume no live network allocation. `--network none` needs no networking tools and preserves the original loopback-only behavior. This version has no host network mode, custom networks, IPv6 routing, or rootless networking.

## Local images

Registry downloads (`mdocker pull`) are not implemented yet. Import a prepared BusyBox directory on the Linux host, then run its stored copy:

```sh
image_id=$(mdocker image import ./rootfs/busybox)
mdocker image ls
mdocker image ls --json
mdocker run --image "$image_id" -- /bin/sh -c 'echo image-run; hostname'
mdocker run -d --name image-worker --image "$image_id" -- /bin/sleep 300
mdocker inspect image-worker
mdocker exec image-worker -- /bin/echo shared-image
mdocker stop image-worker
mdocker rm image-worker
mdocker image rm "$image_id"
```

| Command | Behavior |
| --- | --- |
| `image import DIRECTORY` | Copy a stable BusyBox filesystem into the local image store and print its full content ID. Accept a relative or absolute directory path; reject symlinks in the resolved path components, protected host directories, special files, and mounted descendants. |
| `image ls [--json]` | List images sorted by ID, with architecture, logical regular-file bytes, and UTC creation time. JSON returns an array, including `[]` for an empty store. |
| `image rm ID` | Delete an unreferenced image and print its ID. Refuse deletion while a foreground run holds its lease or any detached container record references it. Missing images return `125`. |
| `run --image ID ... -- COMMAND` | Create a private rootfs copy from a stored image. Use exactly one of `--image` and `--rootfs`; existing resource, user, environment, terminal, mount, timeout, and detach options apply. |

Image IDs use `sha256:` followed by all 64 lowercase hexadecimal digits. IDs include the architecture, sorted paths, copied permission modes, regular file contents, and symlink targets. Timestamps and ownership are excluded. Import strips setuid/setgid bits and assigns files to the importer, just as rootfs preparation does. Importing identical copied content returns the existing ID and creation time. Changing content, modes, links, or architecture changes the ID. This version supports local directory imports for the runtime's native arm64 or amd64 architecture; it has no tags, shortened IDs, tar/archive import, Docker/OCI layers, registry access, or automatic garbage collection.

Keep the source tree unchanged until import completes. Top-level source directories are pinned, and source file access is confined through `os.Root`; file symlinks are preserved rather than traversed, including absolute container links such as `/bin/busybox`. Invalid BusyBox executables and symlinked runtime mount targets fail import. The host root, `/proc`, `/sys`, `/dev`, runtime storage, and their ancestors are rejected. Publication is atomic; failed or canceled imports leave no visible image. Interrupted import/removal staging directories are reclaimed by the next import or successful removal without following symlinks or crossing mount points.

Images live under private, root-owned `/var/lib/mini-docker/images/`. Every container gets a separate working copy, so modifying a container or the original import source does not modify the image or other containers. The import source can be deleted afterward. Stored content is checked against its ID before container creation; damaged images fail startup. Do not edit image storage directly. Raw `--rootfs` paths into image storage are rejected to preserve reference tracking. Directory bind mounts provide persistent writable data alongside an image.

Image operations require Linux and root privileges; import/list do not require a delegated cgroup or systemd. Store publication and deletion use an exclusive lock, while running containers share independent image leases. Concurrent imports deduplicate, and creation racing deletion either retains a protected image or fails with `image not found`. Foreground leases last until runtime cleanup; abrupt supervisor loss releases them because the workload uses an independent rootfs copy. Detached records retain their reference after exit, startup failure, or supervisor recovery, so stop and `rm` all referencing containers before deleting the image. There is no force-delete option.

For image-backed containers, `inspect.config.image` contains the immutable ID and `inspect.config.rootfs` is empty, keeping internal image storage paths private. Directory-backed containers continue to expose their configured rootfs path and omit `image`.

## Persistent data

Create a directory on the Linux host (inside the VM when using Lima):

```sh
mkdir -p "$HOME/mini-docker-data"
mdocker run --rootfs ./rootfs/busybox --read-only \
  --mount "type=bind,source=$HOME/mini-docker-data,target=/data" \
  --workdir /data -- /bin/sh -c 'echo persistent >> result; cat result'
mdocker run --rootfs ./rootfs/busybox \
  --mount "type=bind,source=$HOME/mini-docker-data,target=/data,readonly" \
  -- /bin/cat /data/result
```

`--mount` is repeatable, accepts exactly `type=bind`, `source`, `target`, and the optional bare `readonly` flag, and defaults to writable. Option order is flexible; unknown or duplicate options fail. Paths must be clean absolute paths without `.` or `..` components, trailing slashes, NUL, or newlines; this comma-separated syntax cannot represent paths containing commas. Only existing real source directories are supported, with no symlinks in any path component. Sources are never created automatically. The host root, `/proc`, `/sys`, `/dev`, runtime storage at `/var/lib/mini-docker` and `/tmp/mini-docker-userns`, rootless runtime storage for rootless runs, and sources overlapping the rootfs template are rejected, including ancestors of protected paths.

Targets cannot be `/`, `/tmp`, or overlap `/proc`, `/dev`, or `/sys`. `/tmp/data` is allowed and is mounted after the runtime's temporary filesystem. Missing target directories are created in the private filesystem with mode `0755` (subject to the runtime umask). Existing target directories are covered for this run; files and symlinks in any target component fail startup. Duplicate or nested targets are rejected regardless of option order. Mounting a directory hides the target's previous contents without copying or deleting them.

Bind mounts are nonrecursive: mounted descendants of a source are excluded, and their underlying directories are visible instead. Sources are pinned by directory descriptors during init setup, so later renames do not redirect an installed mount. Mount propagation is private to the container. Every bind enforces `nosuid` and `nodev`, preserves source restrictions such as `noexec` and an existing read-only flag, and applies `readonly` only to the container mount. A writable bind remains writable with `--read-only`; a read-only bind remains read-only with a writable root. The workload cannot remount it after capabilities are dropped.

Host file ownership and permissions are preserved; `--user` uses direct host IDs by default and mapped host IDs when a user namespace is enabled. Grant that user access to the source before running, and avoid mounting sensitive host directories. Writable binds intentionally let the workload modify or delete source data. The runtime never changes source ownership, deletes source directories, or removes their contents during exit, timeout, startup rollback, supervisor recovery, or `rm`. Filesystem writes outside binds remain temporary. Concurrent containers can share a source; ordinary filesystem concurrency rules apply.

The same options work with detached containers. `exec` inherits existing mounts and cannot add new ones. `inspect` includes `config.mounts`, an array of `source`, `target`, and `read_only` objects (empty when no mounts are configured); these explicitly configured host paths remain visible after exit.

## Interactive terminals

Run these commands from a terminal inside the Linux VM:

```sh
mdocker run -it --rootfs ./rootfs/busybox -- /bin/sh
mdocker run -it --rootfs ./rootfs/busybox \
  --user 1000:1000 --read-only --workdir /tmp -- /bin/sh
```

`-i` / `--interactive` attaches stdin. Ordinary foreground runs continue to forward stdin by default; `--interactive=false` supplies `/dev/null` instead. `-t` / `--tty` allocates a PTY. Combine the options as `-it`, `-ti`, or `--interactive --tty` for an interactive terminal. These options apply to foreground execution; detached runs reject enabled interactive or terminal options.

`-it` requires terminal stdin and returns `125` for redirected or piped input. The supervisor temporarily puts that terminal in raw mode, copies input to the container, and restores the original settings after exit, startup failure, or handled termination. Ctrl+C interrupts the container's current foreground job; an interactive shell can continue afterward. Window dimensions are copied before command startup and updated on SIGWINCH. `TERM=xterm` is supplied unless explicitly overridden with `--env TERM=...`.

Each terminal run mounts a private [devpts instance](https://www.kernel.org/doc/html/latest/filesystems/devpts.html), limited to 64 PTYs, and provides `/dev/pts`, `/dev/ptmx`, and `/dev/tty`. The command starts in its own session with a controlling terminal, supporting shell job control. Numeric users and read-only roots remain supported; terminal mounts are writable independently of the root mount. Host PTY devices are not exposed in the container.

Terminal stdout and stderr are combined on stdout, with terminal line discipline such as CRLF newlines. Runtime diagnostics still use stderr. Output is drained after exit with a bounded wait so a blocked consumer cannot prevent terminal restoration. `-t` without `-i` allocates a terminal without forwarding host input and queues an EOF character for canonical readers; when stdin has no window dimensions, the initial size is 24 rows by 80 columns. Normal command exit codes and timeouts retain their existing behavior.

## Background containers

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --user 1000 --read-only --workdir /tmp \
  -- /bin/sh -c 'trap "echo stopped; exit 0" TERM; while :; do echo working; sleep 1; done'
mdocker ps
mdocker logs --tail 5 worker
mdocker stop --timeout 2s worker
mdocker wait worker
mdocker start worker
mdocker restart --timeout 2s worker
mdocker stop worker
mdocker ps --all --json
mdocker logs worker
mdocker rm worker
```

`run -d` (or `--detach`) prints the full container ID after the command starts and returns without waiting for completion. Background stdin is `/dev/null`; stdout and stderr are merged into a private log. All resource and execution options also apply to detached runs. Startup waits at most 95 seconds; failures return `125`, with a retained failed record when allocation succeeded. A command that exits immediately still receives an ID and preserves its exit code.

| Command | Behavior |
| --- | --- |
| `run -d --name NAME ...` | Reserve a unique name of 1-63 letters, digits, dots, underscores, or hyphens, starting with a letter or digit. Names cannot be full container IDs. Omit the name for an automatic `mini-...` name. |
| `ps` | List created, starting, running, and stopping containers. `-a` / `--all` includes exited and failed records; `--json` returns an array for scripts. |
| `inspect ID\|NAME` | Print public configuration, lifecycle state, timestamps, exit status, and resource limits as a JSON object. |
| `stats ID\|NAME` | Print one live memory and CPU sample; `--json` returns an object and `--interval DURATION` sets the CPU sampling window. |
| `wait ID\|NAME` | Wait for the selected execution, print its recorded exit code, and return that code as the CLI exit status. Repeat for completed containers without changing their state. Unknown status returns `125` with a diagnostic and no stdout. |
| `start ID\|NAME` | Start an exited or failed container with the same ID, name, configuration, and retained rootfs. Starting an already running container succeeds without changing its execution. |
| `restart [--timeout DURATION] ID\|NAME` | Stop the selected execution, then start a new one using its retained rootfs. Completed containers can also be restarted. |
| `stop [--timeout DURATION] ID\|NAME` | Send SIGTERM through the supervisor and wait for cleanup. Use the saved `--stop-timeout`, or override this operation with `--timeout` (`0s` through `1m`). Repeated stops of completed containers succeed. |
| `logs ID\|NAME` | Read retained combined output. `--tail N` selects the last N lines; `-f` / `--follow` streams until the selected execution completes, including across a concurrent restart. Interrupting follow leaves the container running. |
| `rm ID\|NAME` | Remove an inactive container's retained rootfs, metadata, configuration, execution receipts, and logs after verifying resource cleanup. Stop active containers first. Removal releases the name for reuse. |

Use full 32-character IDs or exact names. Flags precede the identifier, such as `logs --tail 10 --follow worker`. Successful management commands return `0` except `wait`, which returns the recorded workload status; errors return `125`. The workload's exit status is also available through `ps --all` and `inspect`. Interrupting `wait` returns `130` for SIGINT or `143` for SIGTERM and leaves the workload running.

Each execution has an independent transient systemd service, using [cgroup delegation](https://systemd.io/CGROUP_DELEGATION/). Closing the launcher does not stop it. Persistent records, execution receipts, logs, and private rootfs copies live under `/var/lib/mini-docker/containers/` with private root-owned parent directories. Temporary run state lives under `/var/lib/mini-docker/runs/` and is removed after execution.

`start` and `restart` preserve filesystem changes, ownership, and modes in the private rootfs and preserve data in bind-mount sources. They create fresh namespaces, cgroups, `/proc`, `/dev`, and `/tmp`; temporary files under `/tmp` do not survive. Retained copies no longer require their original rootfs template. Bind sources must still exist, and image references remain reserved until `rm`. An older container created before rootfs retention can start from its original template or image if available, but previously discarded filesystem changes cannot be restored. A failed startup can be retried; startup preparation publishes a complete rootfs atomically and discards incomplete copies.

Executions are numbered from `generation: 0`. Starting a stopped container increments the generation, resets current execution timestamps and exit code, and exposes the preceding result as `previous_exit` in inspection. Exit receipts retain every completed execution until removal. `wait` pins the ID and generation when invoked, so a concurrent restart cannot replace the exit result it is waiting for. Management mutations are serialized per container; an operation queued against an execution that has already changed returns `125` and can be retried. Removal or reuse of a name cannot redirect a pending operation to another container.

The runtime enforces the selected shutdown grace, then kills remaining workload processes and performs bounded cleanup. Systemd provides a 75-second final service boundary, covering the maximum one-minute grace and cleanup if the supervisor cannot finish. The management stop deadline allows the selected grace plus 25 seconds for reconciliation and cleanup.

Logs append across executions and retain a combined prefix up to 16 MiB, including a truncation notice when necessary. Further output is drained and discarded so a full log cannot block the workload; JSON records expose `log_truncated`. This version does not rotate logs or automatically restart containers after a host/VM reboot; use `start` after reconciliation. Management commands reconcile abandoned supervisors using service identity and locks, preserve failed records, and leave the command exit status unknown when an abrupt supervisor loss prevents completion from being recorded.

## Inspection and resource statistics

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --memory 64m --cpus 0.5 --env MODE=demo -- /bin/sleep 300
mdocker inspect worker
mdocker stats worker
mdocker stats --json --interval 500ms worker
mdocker stop worker
mdocker inspect worker
mdocker stats --json worker
mdocker rm worker
```

These commands accept a full ID or exact name for a detached container, including completed and failed containers. They require Linux and root privileges. `inspect` always prints a JSON object. Its `config` contains the rootfs template path or stored image ID, hostname, command arguments, effective working directory and numeric user, read-only and terminal settings, directory bind mounts, and execution and stop timeouts as duration strings. `environment_names` lists the effective variable names, including defaults, without their values. Environment values, raw runtime errors, boot identity, temporary filesystem paths, and cgroup paths are excluded from inspection. Command arguments and the configured rootfs path are intentionally visible, as command arguments already are in `ps`.

`created_at`, `started_at`, and `finished_at` use UTC RFC3339 timestamps. Times that have not occurred and unknown exit codes are JSON `null`. `limits` contains configured `memory_bytes`, `pids`, `cpu_quota_usec`, `cpu_period_usec`, and `cpus`; zero CPU quota and zero `cpus` mean unlimited. Limits and configuration remain inspectable after resource cleanup. `generation`, `previous_exit`, and `filesystem_retained` describe restart history and retained rootfs policy.

`stats` returns a single sample and exits. It samples the workload's cgroups v2 [`memory.current` and `cpu.stat`](https://www.kernel.org/doc/html/v6.8/admin-guide/cgroup-v2.html), including exec descendants and excluding the supervisor. Memory usage includes accounted page cache and kernel memory, rather than only process RSS. CPU utilization is the difference in `usage_usec` divided by the actual elapsed monotonic time between two reads. One fully used core is `100%`; multiple cores can exceed `100%`. The result is not normalized by the host CPU count or the configured quota. The default sampling interval is one second; `--interval` accepts `10ms` through `1m`. Short intervals can be noisy under CPU throttling.

The table shows memory usage and the configured limit in bytes, plus CPU percent. JSON contains `memory_bytes`, `memory_limit_bytes`, `cpu_percent`, cumulative `cpu_usage_usec`, UTC `sampled_at`, and the actual `interval` as a duration string. Missing or invalid counters are independent: available metrics remain visible, unavailable metrics are `null` in JSON and `N/A` in the table, with JSON `memory_unavailable` / `cpu_unavailable` explanations. An idle workload can report a valid `0%`; unavailable metrics never become zero usage. Completed containers have no live metrics or retained historical usage and return immediately with `interval: "0s"`.

State and configuration are read together under a shared storage lock. Sampling pins one cgroup directory without following symlinks and does not hold the storage lock while waiting. If the selected execution completes or restarts during sampling, live metrics are returned as unavailable. Concurrent removal either leaves a complete snapshot or returns `125` with `container not found`; a reused name cannot redirect an in-progress request to another container. Interrupting a statistics request returns the signal's exit status without stopping the workload.

## Execute commands in running containers

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --env MODE=base --workdir /tmp -- /bin/sleep 300
mdocker exec worker -- /bin/sh -c 'hostname; pwd; echo "$MODE"'
mdocker exec --env MODE=check --workdir / --timeout 10s \
  worker -- /bin/sh -c 'pwd; echo "$MODE"'
printf 'hello\n' | mdocker exec -i worker -- /bin/cat
mdocker stop worker
mdocker rm worker
```

`exec ID|NAME -- COMMAND [ARGS...]` requires a running detached container, root privileges, and kernel support for `clone3` with `CLONE_INTO_CGROUP`. Use the full ID or exact name, and place options before the identifier. Foreground runs and inactive or stopping containers cannot accept execution requests.

Each command shares the container's PID, mount, UTS, IPC, and network namespaces and its current filesystem, including writes made by other processes. It inherits the container's configured environment, working directory, numeric UID/GID, capability restrictions, and read-only mounts. Repeatable `--env KEY=VALUE` and `--workdir /PATH` override defaults for that invocation only; host environment variables are never inherited. Identity cannot be overridden with `exec`.

Stdin is `/dev/null` by default; `-i` / `--interactive` forwards caller input. Without `-t`, stdout and stderr go separately to the caller. Exec output is not added to background logs. Command exit codes pass through, signal exits return `128 + signal`, execution errors return `125`, and `--timeout D` returns `124` after its deadline. The timeout starts when the namespace helper launches and includes command startup; `0` means unlimited.

All commands share the container's aggregate memory, process/thread, and CPU limits. Each invocation uses a child cgroup for cleanup, including descendants that create new sessions. Client disconnection, handled termination, and timeout stop that invocation while leaving the main container running. Container shutdown or main-command exit cancels all active invocations. Termination allows a five-second grace period before forced cleanup. The supervisor accepts at most 16 concurrent sessions; request and configuration payloads are limited to 64 KiB. Namespace, root, and executable descriptors remain pinned for the lifetime of the running container.

### Interactive exec terminals

From a terminal inside the Linux VM, start a background container and open a shell:

```sh
mdocker run -d --name worker --rootfs ./rootfs/busybox \
  --user 1000:1000 --read-only --workdir /tmp -- /bin/sleep 300
mdocker exec -it worker -- /bin/sh
# Exit the shell with exit or Ctrl+D; worker keeps running.
mdocker stop worker
mdocker rm worker
```

`-t` / `--tty` allocates a terminal; combine it with `-i` as `-it`, `-ti`, or `--interactive --tty` to attach input. Interactive terminal execution requires terminal stdin and rejects pipes with `125` before creating a session. `-t` alone forwards no input and queues EOF for canonical readers; its initial dimensions default to 24 rows by 80 columns when caller stdin has no terminal size.

Each background container mounts a private devpts instance during startup, bounded to 64 PTYs. Exec sessions allocate independent PTYs in that instance without changing the main process's input or remounting its filesystem. Numeric users and read-only roots are supported. Start new containers after upgrading the runtime to enable terminal execution; older running supervisors cannot add this support.

The client reuses foreground terminal handling: raw input, initial window dimensions set before command startup, SIGWINCH resizing, bounded output draining, and terminal restoration on normal exit, startup failure, timeout, handled termination, or container shutdown. Shell job control is enabled. Ctrl+C interrupts the terminal's foreground job so an interactive shell can continue. Independent exec sessions can run concurrently; exiting or canceling one leaves the main container and other sessions running.

Terminal stdout and stderr are merged on stdout, with terminal newline processing. Runtime diagnostics still use stderr. `TERM=xterm` is the default unless the container environment or an exec `--env TERM=...` overrides it. Environment, identity, working-directory, resource-limit, timeout, and descendant-cleanup rules match ordinary exec.

## Build and validation

| Command | Purpose |
| --- | --- |
| `make build` | Build a static Linux binary at `bin/mdocker` (with a `bin/mini-docker` compatibility symlink) |
| `sudo make install` | Install the built Linux CLI at `/usr/local/bin/mdocker` |
| `make rootfs` | Generate `rootfs/busybox` from installed static BusyBox |
| `make fmt` | Format Go sources |
| `make vet` | Run Go's static checks |
| `make test` | Run unprivileged tests; integration tests are skipped |
| `make test-integration` | Run privileged Linux integration tests in dedicated scopes and services |

`make build GOARCH=amd64` cross-compiles for amd64. `make test-integration` always builds for the Linux VM's native architecture, regardless of inherited `GOOS` or `GOARCH`; the test launcher checks the runtime's ELF architecture. Unit tests and vet also run on macOS. Container execution and rootfs preparation require Linux. `make rootfs` refuses to overwrite an existing destination; remove it explicitly before regeneration. The generated `.mini-docker-rootfs.json` records architecture, package version, and SHA-256 checksum.

Integration tests require the dedicated VM and fail when prerequisites are missing. They exercise execution, input/output, exit status, isolation, privileges, resource limits, signal handling, timeout, child cleanup, repetition, and concurrency. Additional tests verify actual CPU throttling, environment and command lookup, working-directory errors, non-root credentials and cleanup, and read-only roots with writable temporary storage. Background tests cover independent lifetime, retained status and logs, tail/follow/cancellation, log limits, names, concurrent management, bounded stops, removal, and supervisor-loss recovery. Terminal tests verify interactive shell input, job control, Ctrl+C, resizing, private PTYs, restored host settings, input modes, and output draining. Use `./scripts/test-linux.sh -test.run TestTerminal` for a focused terminal run, or `-test.run TestBackground` for background management. Exec tests cover shared namespaces and filesystems, inherited configuration and identity, stream separation, actual exit codes, aggregate CPU limits, concurrent sessions, cancellation, descendant cleanup, launcher removal, and container shutdown. Interactive exec tests also cover job control, resizing, terminal restoration, independent PTYs, and session cleanup. Use `./scripts/test-linux.sh -test.run TestExecTerminal` for terminal exec checks, or `-test.run TestExec` for all exec checks. Inspection and statistics tests cover active, completed, and failed records, configuration privacy, real CPU and memory accounting, idle workloads, unavailable metrics, sampling cancellation, and concurrent exit, removal, and name reuse. Use `./scripts/test-linux.sh -test.run "TestInspection|TestStats"` for these checks. Bind mount tests verify persistence across runs, read-only enforcement, host ownership, writable data with read-only roots, private mount propagation, concurrent containers and exec, excluded submounts, inherited source restrictions, invalid paths, partial startup rollback, timeout, supervisor recovery, and data retention after removal. Use `./scripts/test-linux.sh -test.run TestBindMount` for these checks. Image tests cover import identity and deduplication, source independence, isolated private copies, bind mounts and numeric users, retained references and supervisor recovery, foreground leases, concurrent creation/removal, unsafe sources, special files, mounted subtrees, and partial import cleanup. Image store unit tests also verify cancellation, corrupted content and metadata, concurrent imports/readers, and staging recovery. Use `./scripts/test-linux.sh -test.run TestImage` for image checks. Launcher tests cover automatic scope creation, piped input, argument preservation, exit codes, timeout, capability checks, and failed-delegation recursion prevention. Use `./scripts/test-linux.sh -test.run TestLaunch` for these checks. Lifecycle tests cover wait status and cancellation, retained rootfs and bind data, fresh temporary storage, execution receipts across restart, failed-start retry, stop overrides and deadlines, concurrent starts, unsafe retained mounts, and cumulative log limits. Use `./scripts/test-linux.sh -test.run TestLifecycle` for these checks. Network tests use a separate upstream namespace with no route to container addresses to verify actual masquerading and DNS. They cover host and remote TCP mappings, UDP, loopback restrictions, hairpin access, concurrent allocations, exec and restart, read-only roots and numeric users, port and subnet conflicts, partial-setup rollback with retained reservations, startup rollback, timeout, supervisor-loss and previous-boot recovery, and restoration of interfaces, NAT tables, journals, and forwarding settings. Use `./scripts/test-linux.sh -test.run TestNetwork` for these checks. Resource tests use bounded helpers and deadlines.

## Next milestones

External IPv4 networking is implemented. Seccomp filtering, explicit foreground user namespace mappings, and bounded rootless foreground execution are implemented. Lifecycle extensions are implemented with retained filesystems and per-execution exit receipts. Each addition must retain the foreground and background isolation and cleanup guarantees and pass privileged Linux integration tests.

## Source layout

- `cmd/mini-docker/`: executable entry point.
- `internal/cli/`: argument parsing and environment checks.
- `internal/config/`: execution configuration and validation.
- `internal/container/`: persistent records, background supervision, systemd service management, and bounded logs.
- `internal/runtime/`: supervisor, container init, signals, run state, recovery, and cleanup.
- `internal/rootfs/`: template validation and confined filesystem preparation.
- `internal/image/`: content-addressed local images, import publication, integrity checks, and deletion leases.
- `internal/cgroup/`: cgroups v2 delegation and limits.
- `internal/network/`: IPv4 bridge/veth setup, DNS discovery, NAT and port reservations, and journaled recovery.
- `internal/ipc/`: bounded local messages and close-on-exec descriptor transfer.
- `scripts/`, `dev/`: Linux launchers and development VM configuration.
- `tests/integration/`: privileged Linux behavior tests.

Generated binaries, rootfs templates, and `docs/` are excluded from Git. All project documentation, code, comments, diagnostics, and commit messages use English.
