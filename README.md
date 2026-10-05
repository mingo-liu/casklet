# mini-docker

A small Go container runtime for Linux arm64 and amd64. Run a foreground command in separate PID, mount, UTS, IPC, and network namespaces, with a copied BusyBox root filesystem and cgroups v2 memory and process limits.

## Requirements

Use a dedicated Ubuntu 24.04 development VM with systemd, Linux 6.8 or newer, cgroups v2, root privileges, Go 1.25 or newer, and `busybox-static`. Required cgroup interfaces include `memory`, `pids`, `memory.swap.max`, and `cgroup.kill`. The launcher obtains a delegated systemd scope rather than modifying systemd's top-level cgroups.

This MVP runs trusted programs as container UID 0 with reduced capabilities. It does not provide a security guarantee for untrusted code. There are no image registries, persistent volumes, external container networking, rootless execution, or terminal allocation.

## Development VM on macOS

Install Lima, then start the dedicated VM:

```sh
brew install lima
limactl start --name mini-docker dev/lima.yaml
```

The configuration installs BusyBox, build tools, and checksum-verified Go 1.27.1 from the [official Go downloads](https://go.dev/dl/). [Lima plain mode](https://lima-vm.io/docs/config/plain/) disables host filesystem sharing; transfer a source snapshot into a writable VM-local directory. Run this from the repository root:

```sh
git ls-files -z --cached --others --exclude-standard | \
  COPYFILE_DISABLE=1 tar --no-xattrs --null -T - -czf - | \
  limactl shell mini-docker sh -c \
  'mkdir -p ~/mini-docker && tar -xzf - -C ~/mini-docker'
limactl shell mini-docker
cd ~/mini-docker
make build
make rootfs
```

Repeat the transfer after source changes. The VM keeps build artifacts, rootfs templates, and runtime state on its own filesystem. No host directory is mounted into the VM. On an existing Ubuntu VM, install `busybox-static binutils make` with `apt-get`, and install Go 1.25 or newer before using the commands below.

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

Defaults: hostname `mini`, memory `128m`, process/thread limit `64`, timeout `0` (unlimited). Memory suffixes `k`, `m`, and `g` use powers of 1024. Each run copies the rootfs template, mounts independent `/proc` and temporary storage, and removes its working filesystem after exit. Networking contains loopback only. The command receives a fixed environment and starts in `/`.

Normal command exit codes pass through. Signal exits use `128 + signal`; timeout returns `124`; configuration, unsupported-platform, and startup errors return `125`. Diagnostics go to stderr. SIGINT and SIGTERM are forwarded, with a bounded shutdown grace period.

## Build and validation

| Command | Purpose |
| --- | --- |
| `make build` | Build a static Linux binary at `bin/mini-docker` |
| `make rootfs` | Generate `rootfs/busybox` from installed static BusyBox |
| `make fmt` | Format Go sources |
| `make vet` | Run Go's static checks |
| `make test` | Run unprivileged tests; integration tests are skipped |
| `make test-integration` | Run privileged Linux integration tests in independent delegated scopes |

`make build GOARCH=amd64` cross-compiles for amd64. Unit tests and vet also run on macOS. Container execution and rootfs preparation require Linux. `make rootfs` refuses to overwrite an existing destination; remove it explicitly before regeneration. The generated `.mini-docker-rootfs.json` records architecture, package version, and SHA-256 checksum.

Integration tests require the dedicated VM and fail when prerequisites are missing. They exercise execution, input/output, exit status, isolation, privileges, resource limits, signal handling, timeout, child cleanup, repetition, and concurrency. Use `./scripts/test-linux.sh -test.run TestExecution` for a focused run. Resource tests use bounded helpers and deadlines.

## Source layout

- `cmd/mini-docker/`: executable entry point.
- `internal/cli/`: argument parsing and environment checks.
- `internal/runtime/`: supervisor, container init, signals, run state, recovery, and cleanup.
- `internal/rootfs/`: template validation and filesystem preparation.
- `internal/cgroup/`: cgroups v2 delegation and limits.
- `scripts/`, `dev/`: Linux launchers and development VM configuration.
- `tests/integration/`: privileged Linux behavior tests.

Generated binaries, rootfs templates, and `docs/` are excluded from Git. All project documentation, code, comments, diagnostics, and commit messages use English.
