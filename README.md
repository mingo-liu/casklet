# mini-docker

A small Go container runtime for Linux arm64 and amd64. Run foreground or background commands in separate PID, mount, UTS, IPC, and network namespaces, with a copied BusyBox root filesystem and cgroups v2 memory, process, and optional CPU limits.

Foreground execution, interactive terminals, and background container management are implemented and validated in the dedicated Linux VM. Execution options include configurable environments, working directories, numeric users, and read-only root filesystems.

## Requirements

Use a dedicated Ubuntu 24.04 development VM with systemd, Linux 6.8 or newer, cgroups v2, root privileges, Go 1.25 or newer, and `busybox-static`. Required cgroup interfaces include `memory`, `pids`, `memory.swap.max`, and `cgroup.kill`. Foreground execution uses a delegated systemd scope; detached execution creates its own delegated service. Background management requires `/usr/bin/systemd-run` and `/usr/bin/systemctl`.

Use this runtime for trusted programs. Commands run as container UID 0 by default, with reduced capabilities; `--user` selects another numeric identity. It does not provide a security guarantee for untrusted code. There are no image registries, persistent volumes, external container networking, rootless execution, or execution inside an existing container.

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
```

Repeat the transfer after source changes and use the new printed directory. The snapshot script includes existing tracked and unignored files using null-delimited filenames. Each transfer uses a fresh directory, so deleted source files cannot survive in subsequent builds. Previous snapshots remain available until explicitly removed. The VM keeps build artifacts, rootfs templates, and runtime state on its own filesystem. No host directory is mounted into the VM. On an existing Ubuntu VM, install `busybox-static binutils make` with `apt-get`, and install Go 1.25 or newer before using the commands below.

## Run

```sh
./scripts/run-linux.sh doctor --rootfs ./rootfs/busybox
./scripts/run-linux.sh run --rootfs ./rootfs/busybox \
  --hostname mini --memory 128m --pids-limit 64 --timeout 30s \
  -- /bin/sh -c 'hostname; ps; echo hello'
printf 'hello\n' | ./scripts/run-linux.sh run \
  --rootfs ./rootfs/busybox -- /bin/cat
```

The launcher uses `sudo systemd-run --scope` and preserves standard input and the command's exit status. Override `MINI_DOCKER_BINARY` to use another executable. Arguments after the required `--` are executed directly; explicitly invoke `/bin/sh -c` for shell syntax.

Defaults: hostname `mini`, memory `128m`, process/thread limit `64`, timeout `0` (unlimited). Memory suffixes `k`, `m`, and `g` use powers of 1024. Each run copies the rootfs template, mounts independent `/proc` and temporary storage, and removes its working filesystem after exit. Networking contains loopback only. By default, the command receives a fixed environment and starts in `/`.

### Resource and execution options

```sh
./scripts/run-linux.sh run --rootfs ./rootfs/busybox \
  --cpus 0.5 --env MODE=demo --env EMPTY= --workdir /tmp \
  --user 1000:1000 --read-only \
  -- /bin/sh -c 'id; pwd; echo "$MODE"; echo hello > result; cat result'
```

| Option | Behavior |
| --- | --- |
| `--cpus` | `0` means unlimited; accept `0.01` through `1000`, with up to three decimal places. Set total container CPU bandwidth over a 100ms period; `0.5` allows half of one CPU. Require the delegated CPU controller only when a limit is requested. |
| `--env KEY=VALUE` | Repeat to add variables; the last assignment wins. Accept empty values and `=` in values. Names use letters, digits, and underscores and cannot start with a digit. Override the fixed `PATH=/bin:/usr/bin`, `HOME=/`, and `LANG=C` defaults without inheriting host variables. |
| `--workdir /PATH` | Use an existing directory inside the container; default `/`. Missing or inaccessible directories fail startup. Command lookup uses the configured `PATH` and working directory inside the container. |
| `--user UID[:GID]` | Use numeric IDs from `0` through `4294967294`; GID defaults to UID. Clear supplementary groups and all capability sets before starting the command. The container init uses the same identity to supervise descendants. |
| `--read-only` | Mount the copied root read-only. `/tmp` remains a writable, size-limited tmpfs; minimal `/dev` remains usable. The default root is writable and temporary. |

Numeric users are not user-namespace mappings and do not enable rootless execution. Copied files remain owned by root; custom rootfs templates must grant the selected user access to its working directory and executables. `make rootfs` now generates a traversable root directory; regenerate an older template if its root mode is `0700`.

CPU quotas use cgroups v2 [`cpu.max`](https://www.kernel.org/doc/html/v6.8/admin-guide/cgroup-v2.html#cpu). The supervisor stays outside the workload quota; init, command processes, and their threads share the limit.

Normal command exit codes pass through. Signal exits use `128 + signal`; timeout returns `124` while the main command is running; completed commands keep their exit code during descendant cleanup; configuration, unsupported-platform, and startup errors return `125`. Diagnostics go to stderr. SIGINT and SIGTERM are forwarded, with a bounded shutdown grace period.

## Interactive terminals

Run these commands from a terminal inside the Linux VM:

```sh
./scripts/run-linux.sh run -it --rootfs ./rootfs/busybox -- /bin/sh
./scripts/run-linux.sh run -it --rootfs ./rootfs/busybox \
  --user 1000:1000 --read-only --workdir /tmp -- /bin/sh
```

`-i` / `--interactive` attaches stdin. Ordinary foreground runs continue to forward stdin by default; `--interactive=false` supplies `/dev/null` instead. `-t` / `--tty` allocates a PTY. Combine the options as `-it`, `-ti`, or `--interactive --tty` for an interactive terminal. These options apply to foreground execution; detached runs reject enabled interactive or terminal options.

`-it` requires terminal stdin and returns `125` for redirected or piped input. The supervisor temporarily puts that terminal in raw mode, copies input to the container, and restores the original settings after exit, startup failure, or handled termination. Ctrl+C interrupts the container's current foreground job; an interactive shell can continue afterward. Window dimensions are copied before command startup and updated on SIGWINCH. `TERM=xterm` is supplied unless explicitly overridden with `--env TERM=...`.

Each terminal run mounts a private [devpts instance](https://www.kernel.org/doc/html/latest/filesystems/devpts.html), limited to 64 PTYs, and provides `/dev/pts`, `/dev/ptmx`, and `/dev/tty`. The command starts in its own session with a controlling terminal, supporting shell job control. Numeric users and read-only roots remain supported; terminal mounts are writable independently of the root mount. Host PTY devices are not exposed in the container.

Terminal stdout and stderr are combined on stdout, with terminal line discipline such as CRLF newlines. Runtime diagnostics still use stderr. Output is drained after exit with a bounded wait so a blocked consumer cannot prevent terminal restoration. `-t` without `-i` allocates a terminal without forwarding host input and queues an EOF character for canonical readers; when stdin has no window dimensions, the initial size is 24 rows by 80 columns. Normal command exit codes and timeouts retain their existing behavior.

## Background containers

```sh
./scripts/run-linux.sh run -d --name worker --rootfs ./rootfs/busybox \
  --user 1000 --read-only --workdir /tmp \
  -- /bin/sh -c 'trap "echo stopped; exit 0" TERM; while :; do echo working; sleep 1; done'
./scripts/run-linux.sh ps
./scripts/run-linux.sh logs --tail 5 worker
./scripts/run-linux.sh stop worker
./scripts/run-linux.sh ps --all --json
./scripts/run-linux.sh logs worker
./scripts/run-linux.sh rm worker
```

`run -d` (or `--detach`) prints the full container ID after the command starts and returns without waiting for completion. Background stdin is `/dev/null`; stdout and stderr are merged into a private log. All resource and execution options also apply to detached runs. Startup waits at most 95 seconds; failures return `125`, with a retained failed record when allocation succeeded. A command that exits immediately still receives an ID and preserves its exit code.

| Command | Behavior |
| --- | --- |
| `run -d --name NAME ...` | Reserve a unique name of 1-63 letters, digits, dots, underscores, or hyphens, starting with a letter or digit. Names cannot be full container IDs. Omit the name for an automatic `mini-...` name. |
| `ps` | List created, starting, running, and stopping containers. `-a` / `--all` includes exited and failed records; `--json` returns an array for scripts. |
| `stop ID\|NAME` | Send SIGTERM through the supervisor and wait for cleanup. The runtime uses a five-second grace period; systemd enforces a final whole-service shutdown boundary. Repeated stops of completed containers succeed. |
| `logs ID\|NAME` | Read retained combined output. `--tail N` selects the last N lines; `-f` / `--follow` streams until completion. Interrupting follow leaves the container running. |
| `rm ID\|NAME` | Remove an inactive container's metadata, configuration, and logs after verifying resource cleanup. Stop active containers first. Removal releases the name for reuse. |

Use full 32-character IDs or exact names. Flags precede the identifier, such as `logs --tail 10 --follow worker`. Successful management commands return `0`; errors return `125`. The command's own exit status is available through `ps --all`, independently of the startup and stop commands' status.

Each container has an independent transient systemd service, using [cgroup delegation](https://systemd.io/CGROUP_DELEGATION/). Closing the launcher does not stop it. Persistent records and logs live under `/var/lib/mini-docker/containers/` with private root-owned permissions; temporary rootfs data remains under `/var/lib/mini-docker/runs/` and is removed after execution. Container filesystem writes are temporary, even though logs and metadata survive until `rm`.

Logs retain a prefix up to 16 MiB, including a truncation notice when necessary. Further output is drained and discarded so a full log cannot block the workload; JSON records expose `log_truncated`. This version does not rotate logs or restart containers after a host/VM reboot. Management commands reconcile abandoned supervisors using service identity and locks, preserve failed records, and leave the command exit status unknown when an abrupt supervisor loss prevents completion from being recorded.

## Build and validation

| Command | Purpose |
| --- | --- |
| `make build` | Build a static Linux binary at `bin/mini-docker` |
| `make rootfs` | Generate `rootfs/busybox` from installed static BusyBox |
| `make fmt` | Format Go sources |
| `make vet` | Run Go's static checks |
| `make test` | Run unprivileged tests; integration tests are skipped |
| `make test-integration` | Run privileged Linux integration tests in dedicated scopes and services |

`make build GOARCH=amd64` cross-compiles for amd64. `make test-integration` always builds for the Linux VM's native architecture, regardless of inherited `GOOS` or `GOARCH`; the test launcher checks the runtime's ELF architecture. Unit tests and vet also run on macOS. Container execution and rootfs preparation require Linux. `make rootfs` refuses to overwrite an existing destination; remove it explicitly before regeneration. The generated `.mini-docker-rootfs.json` records architecture, package version, and SHA-256 checksum.

Integration tests require the dedicated VM and fail when prerequisites are missing. They exercise execution, input/output, exit status, isolation, privileges, resource limits, signal handling, timeout, child cleanup, repetition, and concurrency. Additional tests verify actual CPU throttling, environment and command lookup, working-directory errors, non-root credentials and cleanup, and read-only roots with writable temporary storage. Background tests cover independent lifetime, retained status and logs, tail/follow/cancellation, log limits, names, concurrent management, bounded stops, removal, and supervisor-loss recovery. Terminal tests verify interactive shell input, job control, Ctrl+C, resizing, private PTYs, restored host settings, input modes, and output draining. Use `./scripts/test-linux.sh -test.run TestTerminal` for a focused terminal run, or `-test.run TestBackground` for background management. Resource tests use bounded helpers and deadlines.

## Next milestones

Implement non-interactive `exec` next, then reuse terminal handling for `exec -it`. Inspection, resource statistics, persistent data, local images, and external networking remain planned. Each addition must retain the foreground and background isolation and cleanup guarantees and pass privileged Linux integration tests.

## Source layout

- `cmd/mini-docker/`: executable entry point.
- `internal/cli/`: argument parsing and environment checks.
- `internal/config/`: execution configuration and validation.
- `internal/container/`: persistent records, background supervision, systemd service management, and bounded logs.
- `internal/runtime/`: supervisor, container init, signals, run state, recovery, and cleanup.
- `internal/rootfs/`: template validation and filesystem preparation.
- `internal/cgroup/`: cgroups v2 delegation and limits.
- `scripts/`, `dev/`: Linux launchers and development VM configuration.
- `tests/integration/`: privileged Linux behavior tests.

Generated binaries, rootfs templates, and `docs/` are excluded from Git. All project documentation, code, comments, diagnostics, and commit messages use English.
