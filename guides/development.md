# Development guide

[Quick start](../README.md) · [Usage](usage.md) · [Architecture](../ARCHITECTURE.md)

## macOS client

Install Go 1.27.1+, Make, and Lima 2.0+ on macOS 13.5+:

```sh
brew install lima
make build
./bin/casklet doctor
```

`make build` first cross-compiles the native-architecture Linux engine into an
ignored embedded asset, then builds the Darwin client with that engine bundled.
The resulting `bin/casklet` is self-contained apart from Lima. Installing this
file also installs its embedded engine payload; the end user needs neither Go
nor a source checkout. `sudo make install` copies only the macOS executable.
Do not run the client itself with sudo.

Select `GOARCH=arm64` for Apple silicon or `GOARCH=amd64` for Intel. The guest
architecture must match the client. Build each release architecture separately;
`go build` alone does not regenerate the embedded engine after guest changes.

| Command | Purpose |
| --- | --- |
| `make build` | macOS executable with bundled Linux engine |
| `sudo make install` | Install the macOS executable in `/usr/local/bin` |
| `make rootfs` | Export the built-in BusyBox template to `rootfs/busybox` |
| `make fmt` / `make fmt-check` | Format / check Go sources |
| `make test vet test-race` | Unit tests, static checks, and race detection |
| `make vuln` | Reachable vulnerability checks for Darwin/Linux on arm64/amd64 |
| `make test-macos` | Opt-in macOS end-to-end tests through Lima |
| `make engine` | Internal Linux executable for guest development |
| `make test-integration` | Privileged Linux engine suite, inside casklet-runtime |

The macOS suite checks VM stop/start as well as commands and terminals. It skips
the VM reboot test if another active container exists, preserving that workload.

The Go version in `go.mod` is the minimum patched build toolchain and the exact
version selected by CI. Keep it aligned with the verified Go archives in
`scripts/prepare-development.sh` when updating the toolchain. `make vuln` builds
the pinned govulncheck analyzer as a host executable, then scans all four supported
client/engine targets against the current Go vulnerability database. It needs
network access and fails on reachable vulnerable symbols. Review reported
platform and input conditions before treating a finding as an exploit.

The client maintains its initialization lock and generated VM configuration in
`~/Library/Application Support/casklet`. Lima owns the VM disk and SSH
configuration under its own instance directory. The product instance is
`casklet-runtime`, shared by everyday use, development, and tests. Do not create
a second development VM. Development tools are optional guest packages; the
installed product engine continues to be managed by the macOS client.

## Internal Linux engine tests

Linux remains an internal execution and test environment. The ordinary macOS
unit suite excludes Linux-specific tests. Runtime changes must also pass the
privileged suite in the same `casklet-runtime` VM.

Add the guest build tools once, then transfer a fresh source snapshot onto the
VM's Linux disk. Build there rather than on the shared Mac filesystem so Linux
ownership, mounts, and extended attributes work correctly. Guest builds write
only to the snapshot and do not replace `/usr/local/bin/casklet`:

```sh
./bin/casklet machine start
limactl shell casklet-runtime sh < scripts/prepare-development.sh
set -o pipefail
./scripts/snapshot.sh | limactl shell casklet-runtime sh -c \
  'source_dir=$(mktemp -d "$HOME/casklet-source.XXXXXXXX") &&
    tar -xzf - -C "$source_dir" && printf "Source directory: %s\n" "$source_dir"'
limactl shell casklet-runtime
# Change to the directory printed above.
make engine rootfs
make fmt-check test vet test-race
make test-integration
```

Snapshots include tracked and unignored source files, including the embedded
rootfs helper, but exclude generated engines and host state. Use a fresh snapshot
after changes. Engine tests need root, systemd, cgroups v2, namespaces, and network
administration privileges.

Privileged integration tests require exclusive use of the VM. Stop everyday
containers and remove idle named networks first; do not run macOS engine commands,
other privileged tests, or reboot the VM during the suite. The runner refuses
active casklet services, existing casklet bridges/rules, and mounted engine storage.
It pauses the installed restart manager, saves the entire everyday engine store
by rename, and runs against an empty store. On success, failure, or a catchable
signal it restores the original store and manager. Images, cache policies, volumes,
and retained container files are preserved; test cache pruning cannot touch them.
Failed test artifacts remain in `/var/lib/casklet-integration/results` for inspection.
After checking for leaked mounts and services, remove that transaction directory
before retrying. An uncatchable kill or VM shutdown can interrupt restoration:
keep the client stopped, stop any remaining test services, move the unfinished
test store out of `/var/lib/casklet`, and restore `casklet-integration/store` there
and `restart-service` to `/etc/systemd/system/casklet-restarts.service` when present.
Restore `restart-enable` to the service's `multi-user.target.wants` symlink too,
then reload systemd and restart the manager if `manager-active` contains `1`.
Never remove a saved `store` before restoring it.

CI runs formatting, unit tests, static checks, and race detection on macOS and
native Linux amd64/arm64 runners. It builds both macOS client architectures and
their bundled Linux engines, and each native Linux engine. A separate job runs
the four-target vulnerability check.

After those checks pass, the privileged Linux integration suite runs on fresh
GitHub-hosted Ubuntu 24.04 amd64/arm64 VMs. Each job explicitly opts into
`scripts/prepare-integration-vm.sh`, which installs prerequisites, permits only
the test bridge through existing forwarding rules, and prepares rootless user
delegation. Do not use that provisioning script on a shared self-hosted runner.
All jobs run for pushes, pull requests, and manual dispatch; integration failures
fail CI rather than being ignored. Configure these checks as required in the
repository's branch/release rules. macOS end-to-end tests still run locally
through the same Lima VM with `make test-macos`.

## OCI regression tests

The Linux image unit tests serve an in-process registry to exercise platform lists,
layer integrity, cache refresh, and rollback without external registry access.
`tests/integration/oci_linux_test.go` uses a local registry and a relocated static
toolbox, with no `bin/busybox`, to verify defaults, accounts, ownership, offline
execution, retained restart, shared memory, and bounded root capabilities. The
setpriv-style regression drops all bounding capabilities before switching UID/GID.
A separate minimal root test runs with no BusyBox or pre-existing mount targets.
Progress regressions check compressed byte counts, throttling, failure/cancellation
rollback, cache status, and exact stdout results. The macOS progress test starts a
bounded local registry helper inside the product VM and checks pipe output and
host-only stderr PTY output through the actual SSH transport, without public images.
It leaves other running containers and their images alone.

Image transaction unit tests pause a layer stream while cached reads, acquisition,
imports, and unrelated pulls complete. They also cover cancelable same-reference
refreshes, identical concurrent publication, live staging leases, and abandoned
transaction recovery. CoW regressions verify the actual OverlayFS root, separate
writes and whiteouts across restart, sparse storage, read-only execution, and
lower-image leases. `TestImageCopyOnWriteThroughMacClient` exercises those storage
semantics through the real macOS transport. The guest needs OverlayFS and a local
upper filesystem supporting its extended attributes; the product VM uses ext4.
Blob-cache regressions count registry GETs across images, bound simultaneous
downloads, preserve ordered whiteouts, cancel/join workers, and protect cache data
with usage leases during prune. Mac progress tests count actual guest registry
downloads for cold and warm pulls and check both pipe and host terminal output.
Cache-management regressions cover persistent/default/unlimited policies, verified
hit LRU ordering, active lease and download protection, independent prune previews,
image survival after eviction, failed/canceled pull maintenance, unsafe policy
artifacts, and cancelable management locks. Disk tests partition image/cache
allocation without double counting. Privileged CLI checks preserve images during
cache-only cleanup, exercise persisted limits, and verify post-pull eviction;
macOS checks sample status and preview without deleting the user's cache.

Image cleanup regressions pause deletion after its internal lease disappears and
verify that listing, acquisition, import, pull, and same-ID publication still
complete. They cover abandoned legacy transactions, cancellation/deadline/storage
failures, unsafe lock files, symlinks, mounted trees, multi-batch directory removal,
and the 32-transaction recovery descriptor bound. Run the image unit suite as root
in the development VM to include mount preservation and ownership regressions.

For a real public-registry smoke on macOS, use the Redis/Nginx/PostgreSQL examples
in the usage guide. Check application readiness, host-published responses, and
restart data separately from the engine's successful process-start handshake.
Public image smokes require registry connectivity; the deterministic regression
suite does not. Pull staging and caches live on the VM disk.

## Troubleshooting

- `casklet machine status` reports the product VM without creating or starting it.
- `casklet doctor` checks the guest runtime with the default BusyBox template.
- If startup was interrupted, retry `casklet machine start`; engine installation
  is repeatable and executable replacement preserves existing container storage.
- The installed engine is checked against the bundled SHA-256 before reuse.
  A matching version marker alone does not bypass repair of damaged executable
  content. Repair atomically replaces the binary while active supervisors keep
  their pinned executable.
- Every engine command checks the builtin template, including when the bundled
  engine is already installed. Missing/corrupt BusyBox files, applets, metadata,
  or required directories are repaired from the installed static BusyBox package.
  Repair validates a private candidate before atomically replacing the template;
  preparation failure preserves the original. Existing container roots and user
  templates are unaffected. Unsafe ownership, writable directories, symlinks at
  the managed root, or mounts produce an error instead of automatic deletion.
- For a directory outside your shared home, use `machine init --mount` before
  VM creation, or `machine stop`, `machine share DIRECTORY`, and `machine start`
  for an existing VM. Stopping terminates workloads and preserves their files.
- Initialization flags apply only to a new machine. To change an existing VM,
  stop it, use Lima's configuration tools, then start it. Preserve the product's
  dedicated loopback forwarding rules and writable home share.
- Guest diagnostics are available through `limactl shell casklet-runtime`.
  VM image download and Ubuntu package installation require internet access.
- Engine installation enables `casklet-restarts.service` for automatic policies.
  Inspect it with `sudo systemctl status casklet-restarts.service` and
  `sudo journalctl -u casklet-restarts.service` inside the product VM. Engine
  upgrades replace this manager independently of active workload supervisors.
- Report integration checks that could not run. Unit tests and cross-compilation
  alone do not establish terminal, networking, or resource-limit behavior.

### Named network regression checks

The disposable CI VM provisioner permits `casklet0` and the owned named bridge prefix
`csn+` in its existing IPv4 FORWARD chain, because a host DROP policy can override
accepts in separate nftables base chains. Production networking never changes
unrelated forwarding rules. Run `TestNamedNetwork*` in the privileged Linux suite
for DNS over UDP/TCP, scoped aliases, direct-routing separation, upstream NAT,
publishing, restart addresses, foreground leases, retained references, failed
bridge cleanup receipts, and forwarding restoration in both cleanup orders.
Run `TestNamedNetworkServiceDiscoveryAndHostPublish` in the real macOS suite.
`TestProjectNetworkReadinessAndBatchLifecycle` combines scoped discovery,
changed restart addresses, health waiting, host publication, and label-selected
batch cleanup through the client/VM path.
Set `CASKLET_LEGACY_ENGINE` to a baseline engine built before named networking
to include the pinned old-supervisor upgrade regression. Without it that one
case skips explicitly. The Linux runner preserves this setting across sudo.
Root-only network package tests additionally validate metadata ownership, stable
leases, cancellation, and canonical subnet records; other DNS tests run normally.
