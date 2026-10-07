# Development guide

[Quick start](../README.md) · [Usage](usage.md) · [Architecture](../ARCHITECTURE.md)

## macOS development VM

Install Lima and start the dedicated VM:

```sh
brew install lima
limactl start --name mini-docker dev/lima.yaml
```

The [VM configuration](../dev/lima.yaml) installs the Linux prerequisites,
checksum-verified Go, and an AppArmor profile for rootless execution. Plain mode
disables host filesystem sharing. Transfer a fresh source snapshot from the
repository root:

```sh
set -o pipefail
./scripts/snapshot.sh | limactl shell mini-docker sh -c \
  'source_dir=$(mktemp -d "$HOME/mini-docker-source.XXXXXXXX") &&
    tar -xzf - -C "$source_dir" && printf "Source directory: %s\n" "$source_dir"'
limactl shell mini-docker
# Use the exact directory printed above.
cd ~/mini-docker-source.XXXXXXXX
make build rootfs
sudo make install
```

Repeat the transfer after source changes. A fresh directory prevents deleted
files from surviving in later builds. Snapshots include existing tracked and
unignored files; generated artifacts and local notes stay on their own host.
Previous snapshots remain until explicitly removed.

## Build and checks

| Command | Purpose | Where |
| --- | --- | --- |
| `make build` | Static Linux binary in `bin/mdocker` | macOS or Linux |
| `sudo make install` | Install as `/usr/local/bin/mdocker` | Linux |
| `make rootfs` | Generate `rootfs/busybox` from static BusyBox | Linux |
| `make fmt` / `make fmt-check` | Format / check Go sources | macOS or Linux |
| `make test vet test-race` | Unit tests, vet, and race detection | macOS or Linux |
| `make test-integration` | Real namespaces, cgroups, terminals, and networking | Dedicated Linux VM |

Select a binary architecture with `make build GOARCH=arm64` or `GOARCH=amd64`.
Set `ROOTFS=/path/to/template` for rootfs generation and integration checks, or
`PREFIX=/your/prefix` for installation. `bin/mini-docker` and
`scripts/run-linux.sh` remain compatibility entry points.

Integration tests need root, systemd, native Linux binaries, and cgroups v2 with
`memory`, `pids`, `memory.swap.max`, and `cgroup.kill`; CPU limits also need `cpu`.
They change network interfaces and firewall rules, so use a dedicated VM.
Ordinary `make test` skips privileged integration tests and, on macOS, excludes
Linux-specific unit tests. Use the Linux VM to validate runtime changes.

For another disposable Ubuntu 24.04 VM, install Go first and provision as an
unprivileged user with passwordless sudo:

```sh
MINI_DOCKER_DEDICATED_VM=1 ./scripts/prepare-integration-vm.sh
make rootfs test-integration
```

This installs dependencies and the scoped AppArmor profile, enables user
lingering, and delegates controllers to the test user's systemd manager. It
also permits IPv4 forwarding to/from `mdocker0` in the host iptables chain, so
Docker's default `FORWARD DROP` policy on hosted runners does not block the
network fixtures. Other interfaces, policies, and firewall rules are preserved.

Run focused checks after the integration binary and helpers have been built:

```sh
./scripts/test-linux.sh -test.run 'TestImage|TestLifecycle'
./scripts/test-linux.sh -test.run TestNetwork
./scripts/test-linux.sh -test.run TestLaunch
```

[CI](../.github/workflows/ci.yml) runs formatting, unit tests, race detection, vet,
and Linux cross-builds. Privileged suites run in disposable Ubuntu amd64 and
arm64 VMs.

## Troubleshooting

- Run `mdocker doctor --rootfs ./rootfs/busybox` to check runtime prerequisites.
- For missing delegation, check the systemd scope and available cgroup controllers.
- For rootless execution, use a Linux login session with a running systemd user manager and private `/run/user/UID`. See the [security options](usage.md#security-and-rootless-execution).
- Regenerate older BusyBox templates if a numeric non-root user cannot traverse the root directory.
- Report failed checks and checks that could not run when submitting changes.
