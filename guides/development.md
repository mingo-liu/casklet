# Development guide

[Quick start](../README.md) · [Usage](usage.md) · [Architecture](../ARCHITECTURE.md)

## macOS client

Install Go 1.27.3+, Make, and Lima 2.0+ on macOS 13.5+:

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

`make build` and `make engine` stamp the same `VERSION` value into the client
and guest engine. An exact `v[0-9]*` tag supplies the default; other source builds
use `dev`. Override explicitly with `make build VERSION=v0.1.0`. The release
workflow passes its selected tag through `CASKLET_RELEASE_VERSION` so other tags
on the same commit cannot change the selected release version. Standard Go VCS
metadata supplies the full commit and modified flag without build timestamps.
When VCS stamping is unavailable, version output reports `unknown` rather than
inventing a commit. `go build` alone preserves local version queries but can
report a missing/stale bundled engine; always use `make build` for delivery.

CI builds a stamped native executable, queries both version aliases with Lima
absent from `PATH` and invalid machine-state configuration, and independently
checks its commit, modified flag and actual embedded engine SHA-256. Native Linux
CI verifies the internal engine's stamped version too.

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

Disposable macOS validation can pair a separate `LIMA_HOME` with an absolute
`CASKLET_MACHINE_STATE_DIR` inside the VM's writable home share. This isolates
host engine staging and VM state while preserving the real `HOME`, which native
Keychain access requires. Set both variables together; overriding `HOME` can
make the login Keychain unavailable.

## Internal Linux engine tests

Linux remains an internal execution and test environment. The ordinary macOS
unit suite excludes Linux-specific tests. Runtime changes must also pass the
privileged suite in the same `casklet-runtime` VM.

Linux race tests require a C compiler; `scripts/prepare-development.sh` installs
GCC alongside Go and Make.

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
repository's branch/release rules. Local macOS end-to-end tests run through the
same Lima VM with `make test-macos`; dedicated CI uses the isolation described below.

Both privileged CI architectures also build the pre-named-network engine at
`e470555a6df39f95823c1ab50dc9978f335b38f5` with the current patched Go toolchain.
The mixed-generation supervisor regression must execute: missing baselines,
wrong architectures, or an occupied bridge journal fail rather than skip.
This covers an old running supervisor's cleanup alongside the new engine's named
network; it is not a general downgrade guarantee. Change the pinned revision only
when deliberately changing the supported compatibility baseline.

Locally, set `CASKLET_LEGACY_ENGINE` to an absolute native Linux baseline binary
and `CASKLET_REQUIRE_LEGACY_ENGINE=1` before `make test-integration` to enforce
the same check through the data-preserving shared-VM wrapper. Without these
settings the old-supervisor case remains optional for ordinary local runs.

## Manual macOS end-to-end release validation

Before each release, run the complete `make test-macos` client/VM suite manually
on a native Apple silicon Mac. An existing development Mac and its regular user
account are sufficient; no additional Mac, macOS account, GitHub runner registration,
Actions variable or environment is required. Rosetta is rejected for release tests.
Use macOS 13.5+, Lima 2.0+, Make, Python 3.9+, the Go version in `go.mod`, and network
access to Ubuntu images and packages. Keep the Mac awake and online until cleanup
finishes, and run only one full VM suite at a time.

GitHub-hosted Actions continue to run formatting, unit, vet, race, build,
vulnerability, native macOS package and native Linux arm64/amd64 integration checks.
The real client/VM suite runs locally because GitHub-hosted Apple silicon runners
do not support the nested virtualization needed to start the product VM; see
the [GitHub-hosted runner limitations](https://docs.github.com/en/actions/reference/runners/github-hosted-runners#limitations-for-arm64-macos-runners).
Intel retains native archive builds, offline package verification and native Linux
amd64 integration coverage. Full Intel macOS client/VM execution is not required.

`scripts/test-macos-ci.py` creates a fresh short path under `/private/tmp` for
`LIMA_HOME` and a private temporary directory inside the ordinary shared home for
`CASKLET_MACHINE_STATE_DIR`. The latter changes the host initialization lock,
generated configuration, and engine staging directory. It requires an absolute
path within a configured writable share so the installer can transfer its payload.
Always pair the two when testing an independent machine: changing host state alone
does not isolate the VM. The ordinary home share remains available for live-bind tests.
This is a disposable product VM, not a second permanent development VM; the
script never adopts or stops an inherited Lima home or daily product VM.
It enables `CASKLET_MACOS_RESIZE_GROW=1` so the opt-in disk-growth
regression can exercise real expansion once available, only on this temporary VM.
The account's ordinary home remains shared writable for installation and live-bind
tests, as it is during ordinary product use.

The suite has a 35-minute outer deadline, retains the existing Go test timeout,
and terminates its command group on timeout or catchable signals. Bounded
diagnostics collect suite output, the instance listing, recent guest journal,
and up to 20 Lima logs (last 4 MiB each). Cleanup forcibly stops and deletes only
the isolated `casklet-runtime`, then removes both temporary directories. Local
artifacts retain logs even when tests fail; disks, SSH keys and credential files
are excluded. Inspect logs before attaching them to a release.

If teardown fails, the job fails and preserves the temporary directory recorded
in `run-directory.txt` and `state-directory.txt`. An uncatchable kill, host shutdown, or job cancellation
can also leave a VM behind. Before rerunning the suite, verify the recorded path
belongs to this run, then use `LIMA_HOME=/private/tmp/ck-e2e-EXAMPLE/lima limactl
delete --force casklet-runtime` and remove only those run and state directories after the VM
has stopped. Never run recovery against the default Lima home. Check for
abandoned runs after a host interruption; the script does not
delete unknown old directories automatically.

After creating the candidate tag, test that exact clean revision and version:

```sh
release_tag=v0.1.0-rc.1
git fetch origin main --tags
git switch --detach "$release_tag"
test -z "$(git status --porcelain)"
tested_revision=$(git rev-parse HEAD)
CASKLET_RELEASE_VERSION="$release_tag" \
CASKLET_MACOS_EXPECT_ARCH=arm64 \
CASKLET_MACOS_REQUIRE_NO_SKIPS=1 \
CASKLET_MACOS_CI_ARTIFACTS="$PWD/bin/macos-release-$release_tag" \
python3 scripts/test-macos-ci.py
```

Continue only after the command exits zero, with no skipped tests and successful
VM cleanup. Record the tag, full `tested_revision`, test date, native architecture,
and pass/no-skip result in the draft release notes; attach the reviewed `suite.log`
from the artifact directory. Any source change or moved tag requires testing the
new candidate again. A previous development test run is not validation for a
different release revision or version.

Artifacts default to `bin/macos-e2e-artifacts`; set `CASKLET_MACOS_CI_ARTIFACTS`
to a separate directory per run when retaining multiple runs. Set
`CASKLET_MACOS_CI_TIMEOUT_SECONDS` (60–3600) to adjust the outer deadline.
The fake-runner checks cover success, launch/bootstrap/test failure, deadline, SIGTERM, cleanup
failure, preservation of inherited daily state, and exclusion of SSH keys. They
run in ordinary hosted CI without virtualization. A passing fake/unit check does
not establish that the real VM suite passed.

## Release publication gate

Publish through `.github/workflows/release.yml` from `main`. Prepare an existing
tag whose commit is already merged into `main`, and an unpublished GitHub draft
release for that tag. Tags use `vMAJOR.MINOR.PATCH` with an optional prerelease or
build suffix (for example `v0.1.0-rc.1`). After the manual validation above succeeds,
start the workflow with the exact tested commit:

```sh
gh workflow run release.yml --ref main \
  -f tag="$release_tag" -f macos_tested_revision="$tested_revision"
```

Providing `macos_tested_revision` declares that the complete native Apple silicon
suite passed manually without skips for this tag. Preparation requires its full
40-character lowercase commit SHA to match the tag's commit, records the declaration
in the workflow summary, and refuses non-main dispatches, missing drafts and
unmerged tags. This is a maintainer declaration: Actions verifies the revision,
not the local test execution or attached log. No self-hosted job is queued.
Hosted formatting/unit/vet/race/build/vulnerability and both privileged Linux
suites then run against the selected immutable commit. Failed, cancelled or
skipped hosted checks cannot publish.

Only the final hosted publication job receives `contents: write`. It rechecks
the draft release ID, exact tag object, target commit and main ancestry before
changing `draft` to `false`. A moved tag or replaced draft requires a new run.
The hosted checks also build native downloadable packages on `macos-15` (arm64)
and `macos-15-intel` (amd64). Trusted packaging controls build the selected source
and version, then execute the archived client offline to verify its version,
commit and bundled Linux engine bytes. Archives contain `casklet`, `LICENSE`, both READMEs, `ARCHITECTURE.md`, and the
usage/development guides under `guides/`, preserving their relative links for
offline reading. Packaging checks every inline local Markdown link against the
archive members. Local configuration, private notes, and standalone guest engines
are excluded.
Each architecture's transfer checksum is checked before generating `SHA256SUMS`.
Only the final job uploads both archives and that manifest to the existing draft,
replacing same-named assets on a retry. It rechecks draft/tag identity again after
upload before publishing. Missing/corrupt packages or failed uploads leave the
draft unpublished; a partial upload can be retried. Other draft assets are retained.
The binaries are not Developer ID signed or notarized. These hosted package
checks do not replace the required manual Apple silicon VM suite. Intel package
verification does not establish Intel client/VM end-to-end coverage.

The same package build runs on every CI invocation. For local native verification
from a clean checkout (output stays ignored), run:

```sh
python3 scripts/release_assets.py --project . --output bin/packages --version v0.1.0 --arch arm64
```

Use `amd64` on an Intel Mac. Build each archive on its native host.
GitHub writers who publish directly through the UI/API bypass this workflow, so maintainers must use this publication path and protect release refs.
See the [GitHub reusable workflow permissions](https://docs.github.com/en/actions/reference/workflows-and-actions/reusing-workflow-configurations)
and [release API](https://docs.github.com/en/rest/releases/releases#update-a-release).

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
- A running VM is not sufficient evidence of completed initialization. Before
  installing/reusing the engine, the client checks the provisioning marker and
  required guest tools/static BusyBox package. Incomplete setup or missing tools
  rerun the shared provisioning script under a guest lock, without rebooting the
  VM. Ubuntu package downloads retry three times with 30-second network timeouts;
  package-lock waits are bounded to 60 seconds, and each guest repair has a
  nine-minute deadline plus ten seconds for forced termination. Restore network
  access and retry `machine start` after a failure. Healthy setup skips package
  installation; repairing packages preserves an already delegated user manager.
  Project preview requires completed initialization and reports a start hint
  rather than installing guest dependencies.
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
  use `machine stop`, `machine resize --cpus 6 --memory 8 --disk 40`, and
  `machine start`. Omitted resources remain unchanged; disk shrinking is rejected.
  Resize preserves dedicated loopback forwarding rules, shares, and existing data.
  `machine status` reports configured resources; changes apply on the next start.
- Guest diagnostics are available through `limactl shell casklet-runtime`.
  VM image download and Ubuntu package installation require internet access.
- Engine installation enables `casklet-restarts.service` for automatic policies.
  Inspect it with `sudo systemctl status casklet-restarts.service` and
  `sudo journalctl -u casklet-restarts.service` inside the product VM. Engine
  upgrades replace this manager independently of active workload supervisors.
- Report integration checks that could not run. Unit tests and cross-compilation
  alone do not establish terminal, networking, or resource-limit behavior.

### Machine resize regression checks

`TestMachineResizePreservesDataAndGrowsFilesystem` verifies resource changes and
retained container, image, and volume data across disk growth. It is skipped in
ordinary `make test-macos` runs because disk growth cannot be undone. Enable it
only in a disposable, isolated VM environment with `CASKLET_MACOS_RESIZE_GROW=1`
and an absolute non-default `LIMA_HOME`. The test rejects the default Lima home,
requires no active containers, and adds 1 GiB to the isolated machine's disk.
The runner must destroy that disposable VM after testing.

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

## Project readiness sampling benchmark

This opt-in benchmark creates and removes bounded 16/64-service projects in the
product VM. Run it without other VM tests or workloads that need predictable
latency; teardown preserves unrelated retained records. Setup is excluded from
timing, and each measured sample checks the original full IDs/generations.

```sh
make build
CASKLET_MACOS_BENCHMARK=1 CASKLET_MACOS_BINARY="$PWD/bin/casklet" \
  go test -run '^$' -bench BenchmarkProjectReadinessSampling \
  -benchtime=3x -timeout 8m ./tests/macos
```

The two strategies compare the former per-service `inspect` transport with one
project-scoped `__project-status` read. This measures one readiness pass, not complete
startup, dependency waits or image preparation. Use 1-10 iterations to keep live
sampling bounded. Initial per-service configuration/healthcheck inspection and
final deployment validation remain outside the optimized wait loop.
