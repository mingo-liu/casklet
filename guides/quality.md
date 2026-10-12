# Completed quality work

[Development](development.md) · [Architecture](../ARCHITECTURE.md)

Use this record when reviewing the project. Check the current implementation
and regression tests before proposing an item already listed here. Reopen an
item only with a reproducible regression or a distinct uncovered scenario.

## 2026-10-12: Read only requested initial log tails

Initial `logs --tail` selection now happens under the existing log snapshot lock,
before payload allocation and reads. All retained artifacts are still opened and
validated. A backward 32 KiB scan handles rotated segments, empty current files,
partial and long lines, and terminal newlines. Only the selected suffix is copied;
zero tails pin the current end without reading historical content. The existing
incremental follow cursor, inode pin, retention-overrun behavior, legacy lock
compatibility and generation fences remain unchanged.

Regressions cover rotated/empty/split-line tails, zero/full/oversized requests,
large retained payloads with small returned allocations, zero-tail follow through
rotation, skipped unsafe artifacts, cancellation and changed generations. A real
macOS regression verifies rotated and empty tails. Separate full-snapshot and
initial-tail benchmarks use four 4 MiB segments.

Validation:

- macOS arm64 formatting, full unit tests, vet, full race checks and native
  client/embedded engine build passed.

## 2026-10-11: Order-independent project network alias fingerprints

Project service fingerprints sort a copy of network aliases so equivalent alias
sets retain the same identity. Generated run arguments keep their original order;
adding/removing aliases and changing other execution settings still count as drift.

Existing services retain their immutable labels and ordered-alias hashes. On a
hash mismatch, matching project/service ownership permits inspection of the exact
container ID. The desired full configuration is hashed with the saved alias order
only after its alias set matches, excluding a store-appended container name when
it was not explicit. This supports running reuse and stopped-service resumption
without recreation while preserving ownership and configuration drift checks.
No environment values are obtained from inspection or added to ownership state.

Regressions cover reordered and automatically added service aliases, added/removed
aliases, unchanged run arguments, a pinned pre-fix hash, legacy running/stopped
reuse, environment/command drift, wrong ownership/inspection identity, inspection
errors, and explicit container-name aliases. The macOS deployment regression also
reorders aliases and checks unchanged container IDs/generations plus all DNS names.
Focused help, usage, architecture, and bilingual quick starts describe the behavior.

Validation:

- The alias-order regression failed against the original implementation.
- macOS arm64 full unit tests, vet, formatting, native client/embedded engine build,
  and CLI/project race checks passed.
- The complete disposable macOS arm64 suite passed all 50 tests, including
  unchanged IDs/generations after alias reorder, DNS resolution, project rollback,
  cancellation, storage, health, machine resize and terminals. The runner deleted
  its isolated VM and host state. Intel Mac execution was not performed locally;
  privileged Linux validation is provided by the PR's disposable hosted CI suites.

## 2026-10-11: Declarative project deployment and bounded rollback

Added local strict JSON `project up --config`, `project ps`, and `project down`.
A bounded acyclic dependency graph supports started/healthy conditions, scoped
networks/volumes and service DNS aliases. The host expands paths and env-files
before VM changes and fingerprints effective explicit run values. Project-owned
names, detach mode and reserved labels cannot be overridden. Overlapping ports,
alias conflicts, disabled required health checks and configuration drift fail
before deployment mutations. Matching running services are reused and stopped
services resume through the existing lifecycle path.

The orchestrator calls existing CLI paths, preserving Keychain authentication,
Mac path/share translation, host-port checks, health waits and generation fencing.
A cancelable local per-project lock serializes mutations; read-only status remains
available during health waiting. Synced private ownership receipts contain resource
names/timestamps only, not run arguments or environment values. Private guest
resource operations refuse adopting existing names and compare creation receipts
under the deletion metadata lock before reference/lease/mount checks.

Startup failures/cancellation use a bounded independent cleanup context and
transaction labels to remove only this invocation's new containers and verified
resources. Existing resources and resumed services stay. Down pins container IDs,
aggregates partial failures, removes owned networks and preserves data volumes
unless --volumes is explicit. Replaced/unverified resources are preserved.
Abrupt transport/process loss and lost local receipts require explicit inspection
rather than inferred resource ownership. Schema, examples, help parity, architecture,
and bilingual quick starts describe these boundaries.

Regressions cover duplicate/unknown/null fields, graph cycles, logical resource
validation, local file expansion, stable/drifted fingerprints, explicit image env
overrides, aliases/ports, ownership mismatches, exclusive resource creation,
conditional deletion, repeated startup, existing-service preservation, rollback,
cancellation, receipt persistence, unsafe artifacts, lock cancellation and partial
cleanup retries. Privileged Linux adds real project deployment/data retention;
Mac adds DNS/readiness/reuse/drift, volume retention and failure/cancel cleanup.

Validation:

- macOS arm64 full unit tests, vet, formatting, native client/embedded engine build,
  and project/CLI race checks passed.
- After synchronizing the statistics feature from main, the complete disposable
  macOS arm64 suite passed all 50 tests in 181.953 seconds, including project-label
  metrics, pending-dependency status, cancellation cleanup, readiness/DNS, repeated
  deployment and retained data. The runner deleted its isolated VM/host state;
  the daily machine stayed running.
- The network/volume receipt regressions also passed as root in that disposable
  Linux arm64 VM using native cross-compiled test executables. They verify refusal
  to adopt existing resources and preserve replacements with different creation
  identities, including successful/idempotent removal of the verified volume.
- Hosted CI passed full native Linux amd64/arm64 unit/vet/race/build checks,
  vulnerability scanning and both complete privileged integration suites for the
  initial project feature revision 08f16df. The synchronized PR retains the same
  required gates. Intel Mac execution was not performed locally.

## 2026-10-11: Multi-container statistics and continuous monitoring

Added multiple operands, retained-container all/filter selection, and `stats --watch`.
Selections resolve and deduplicate immutable IDs once. The engine pins each cgroup,
reads first counters, waits once across the set, reads final counters, and fences
execution generations. Removed rows stay unavailable; later frames reacquire new
execution readers without combining counters. CPU/memory null semantics and the
single-operand JSON object remain compatible. Selectors/multiple operands emit
arrays; watch uses newline-delimited JSON or successive plain tables over one SSH
session. Empty selections exit, and unavailable frames remain interval-limited.

Regressions cover parser conflicts, selector filtering/sorting, duplicate IDs,
fixed identities, JSON frames, empty selection, cancellation, and idle throttling.
Linux integration adds two-container real-counter/common-window verification.
The real Mac test covers multiple metrics, streaming frames and interrupt status
without terminating workloads. Help, architecture and bilingual documentation agree.

Validation:

- macOS arm64 full unit tests, vet, formatting, native client/embedded engine build,
  and CLI/container/cgroup race tests passed.
- The complete disposable macOS arm64 suite passed in 164.937 seconds, including
  multi-container watch, VM lifecycle, storage, health and terminal regressions.
  The runner restored no daily state because its Lima/host state were isolated.
- Hosted PR CI passed full unit/vet/race/build checks on macOS and native Linux
  amd64/arm64, the four-target vulnerability scan, and both complete privileged
  Linux integration suites. Intel Mac execution was not performed locally.
- A documentation-only rerun encountered existing log-rotation-follow and
  supervisor-loss cleanup fixture failures on different runner architectures.
  The failed jobs passed unchanged on retry; the new multi-sampling regression
  passed in every run. No retention or cleanup assertion was weakened.

## 2026-10-11: Private OCI registry authentication

Added macOS `login --username USER --password-stdin [REGISTRY]` and idempotent
`logout [REGISTRY]`, with focused help and Docker Hub alias normalization. Login
saves credentials without claiming to validate access. Native Keychain entries
are scoped by registry authority and port; native Security/CoreFoundation calls
preserve static client builds without credential helper subprocesses. Locked or
denied Keychain access fails explicitly; only absent entries allow anonymous pulls.

A bounded, cancellable non-PTY host/guest preparation exchange resolves automatic
runs from the cache before reading Keychain and supplies credentials only for
an uncached pull. The resulting immutable image ID is passed to the normal
workload session, preserving stdin, terminals, signals, and remote cleanup.
Credentials stay out of arguments, files, container metadata, and environment.
Explicit pulls refresh with scoped credentials. Authenticated errors are sanitized,
including reflected server bodies and wrapped cancellation. Basic/Bearer support
restricts credentials to the registry and its declared token endpoint; cross-port
blob redirects strip authorization, and token POST redirects outside scope fail.
Authenticated endpoints require HTTPS except loopback development HTTP. Only
Bearer realms are interpreted as token URLs; Basic realms remain challenge labels.

Regressions cover canonical scopes, password/envelope bounds, exact bounded JSON,
fragmented protocol output, broken pipes, cancellation, native Keychain CRUD,
Basic/Bearer pulls, credential reflection, redirect isolation, plaintext endpoint
rejection, cached offline runs, guest preparation, help parity, and workload input.
Usage, architecture, and both READMEs describe the feature and limitations.

Validation:

- Go 1.27.2 formatting, macOS unit tests/vet, native release build, and CLI/machine/
  credential race tests passed. Native Keychain CRUD passed with temporary entries.
  Darwin amd64 client/embedded engine cross-build and the four-target vulnerability
  scan passed; Intel Mac execution was not performed locally.
- Linux arm64 formatting, unit tests, vet, full race detection, and the complete
  privileged integration suite passed in the shared VM. After the final realm
  guards, image/credential race tests and credential preparation plus the pinned
  `e470555` legacy supervisor integration regressions passed again.
- The complete disposable macOS arm64 suite passed all 45 tests in 136.662
  seconds with Go 1.27.2, including Keychain login/pull/automatic run/logout,
  cached terminal operation, exact workload stdin, and health-wait cancellation.
  The runner exited zero and deleted its isolated VM, Lima home, and host state.
  Signal forwarding preserves the original interrupt without duplicate
  context-driven SIGTERM delivery.
- Daily engine, hash marker, restart service, and retained Redis/Tomcat metadata
  were verified unchanged after the shared-VM tests. Full macOS verification uses
  a disposable VM with separate Lima/host state directories and the real HOME,
  preserving Keychain access and daily state.

The minimum Go version and guest development archive pins now use Go 1.27.2 to
resolve the existing four-target vulnerability gate findings. Guest tooling also
installs GCC for race detection. The shared integration wrapper now inspects
restart-manager LoadState before stopping it and enumerates loaded test units
before cleanup, covering disposable VMs without a preinstalled manager. Eight
fake-systemd scenarios verify success, test/inspection/stop failure propagation,
and daily-data restoration, and run in ordinary CI.

## 2026-10-11: Resize existing stopped product machines

Added `machine resize --cpus/--memory/--disk` with focused help and matching
`help machine resize`. Omitted resources stay configured; CPU/memory may increase
or decrease, while disk shrinking is rejected before edits. A stable machine lock
serializes resize with creation, startup, stop, and shares. Missing, running,
transitional, plain, incompatible, and unavailable-resource instances fail without
implicit startup or shutdown. Current values are accepted as a no-op while stopped.

Edits use Lima's resource flags against the existing instance, preserving the VM
disk and unrelated shares, forwarding, and provisioning. `machine status` displays
configured resources. Lima applies resource changes and disk growth at the next
start; Ubuntu cloud-init grows the root partition/filesystem. Checked this behavior
against the installed Lima 2.2.1 and the supported 2.0.0 source, including
[the disk startup path](https://github.com/lima-vm/lima/blob/v2.0.0/pkg/instance/start.go)
and [the root growpart configuration](https://github.com/lima-vm/lima/blob/v2.0.0/pkg/cidata/cidata.TEMPLATE.d/user-data).

The minimum Go version and guest development archive pins now use Go 1.27.2,
which fixes standard-library vulnerabilities reported by the existing four-target
`make vuln` gate with Go 1.27.1. Archive checksums match the official Go downloads. Guest development setup
installs GCC as required by Linux race tests. Hosted Linux integration initially
failed before tests because a fresh VM has no restart manager unit. The shared
runner now skips that stop only for an inspected `not-found` unit and cleans up
only enumerated loaded test units; actual inspection/stop failures stay visible.
Eight fake-runner scenarios verify absent/present units, failure propagation, and
daily-store/manager preservation, and run in the ordinary CI check jobs.

Regressions cover parser bounds, omitted values, reductions, fractional disk shrink,
state/compatibility refusal, edit errors, lock cancellation, idempotence, focused
help parity, and configured status. An opt-in macOS test uses a disposable
non-default Lima home to verify actual CPU/memory application, root filesystem
growth, unrelated configuration preservation, and retained container/image/volume
data after restart. Ordinary daily-use tests skip irreversible disk growth.

Validation:

- `make fmt-check test vet build` passed on macOS arm64 with Go 1.27.2.
  Darwin amd64 client/embedded Linux amd64 builds also passed, and the native
  arm64 binary was restored. Machine/CLI race checks passed.
- `make vuln` passed for Darwin/Linux on arm64/amd64 with Go 1.27.2.
- The isolated macOS arm64 resize regression passed against Lima 2.2.1: CPU and
  memory reductions applied, the configured disk grew from 20 to 21 GiB and
  the root filesystem expanded, unrelated configuration stayed intact, and retained
  container/image/volume data survived. CPU/memory were restored for later tests.
  The disposable VM was stopped and deleted afterward. Daily VM resources and
  storage were not changed.
- Hosted CI passed all ordinary checks and full privileged Linux integration on
  amd64 and arm64. The full macOS suite and Intel Mac execution were not run
  locally for this host-only resource editor change.

## 2026-10-11: Disposable macOS end-to-end CI

Added an opt-in weekly, release, and manual workflow for a dedicated Mac runner.
Pull requests and ordinary pushes never select the persistent runner. A hosted
gate requires the immutable source revision to be merged into `main`; manual
dispatch is restricted to `main`. The runner requires a configured `macos-e2e`
environment, queues concurrent runs, and checks out without persistent credentials.

The reusable runner gives each full `make test-macos` invocation a fresh short
`LIMA_HOME` and absolute `CASKLET_MACHINE_STATE_DIR`, preserving the default
host state and daily VM. It enables opt-in resize growth only in this disposable
VM. An outer deadline and catchable signals terminate the command group; bounded
diagnostics run before forced VM stop/deletion. Cleanup failure fails the run and
retains its directory for explicit recovery. Artifacts include test/guest/Lima
logs, excluding VM disks, SSH keys, and credential files. Setup and interruption
recovery are documented, with bilingual README pointers.

The first hosted Linux CI run reproduced an uncovered disposable-VM case: an
absent installed restart manager made `systemctl stop` fail before tests. The
shared wrapper now skips that stop only for an explicitly inspected `not-found`
unit and enumerates actual loaded units before cleanup, avoiding empty wildcard
stops. Inspection and real stop failures still fail and preserve recovery data.
The guest development setup also installs GCC, required by Go race checks on a
fresh image.

Validation:

- Go 1.27.2 macOS arm64/amd64 client and embedded Linux engine builds, formatting,
  unit tests, vet, and machine package race checks passed. Actionlint passed for
  both workflows and the custom runner label. The minimum Go version and verified
  development archive pins were updated because Go 1.27.1 exposed reachable
  standard-library vulnerabilities in the existing CI scan. The final four-target
  `make vuln` scan found no reachable vulnerabilities with Go 1.27.2.
- Fake-runner regressions passed success, suite failure, launch/bootstrap failure,
  timeout, SIGTERM, teardown failure, daily-state preservation, and SSH-key
  exclusion. Ordinary macOS CI runs these checks without needing virtualization.
  Eight additional fake-systemd cases exercise the full relocated Linux wrapper
  for absent/present managers, populated/empty unit inventory, test failure,
  inspection failure, and manager/test stop failures, including restoration of
  daily data and enabled-service links or safe retention of original backups.
- The real disposable macOS arm64 suite passed all 44 tests in 149.128 seconds,
  including VM stop/start, host ports, terminals, repair, images, and signals.
  The runner exited zero and deleted its VM, Lima home, and separate host state.
  An initial cold run failed before the suite because engine staging was outside
  the writable share; its VM was also deleted and its logs preserved. Moving
  isolated staging inside the shared home fixed that gap without touching daily
  state. The full VM run used Go 1.27.1; final builds and static/unit checks used
  Go 1.27.2 after the security baseline update.
- GitHub workflow execution requires the dedicated runner and environment to be
  configured; no hosted end-to-end job or Intel Mac execution was claimed.

## 2026-10-10: One Lima VM for everyday use, development, and tests

Development now reuses `casklet-runtime` instead of creating a separate plain
`casklet` VM. Optional Go/Make setup lives in `scripts/prepare-development.sh`;
source snapshots and guest build output stay on the VM's Linux disk. Removed
`dev/lima.yaml` and aligned the development guide, architecture, repository
instructions, and bilingual READMEs with this workflow.

The privileged Linux runner requires exclusive VM use and refuses active casklet
services, existing bridges/rules, and mounted engine storage. A stable lock excludes
concurrent integration runners. It temporarily saves the everyday engine store,
installed restart unit, and its boot-enable link; tests use a fresh store. Cleanup
restores daily data and the manager on success, test failure, and catchable signals.
Failed artifacts remain for inspection, and interrupted-run recovery is documented.
The boot-enable link must be absent during testing so systemd can unload transient
restart managers between cases. Cleanup reloads systemd before restoring that link
to prevent restarting the test executable as the daily manager.

Validation:

- macOS arm64 build, formatting, unit tests, and vet passed. Linux arm64 engine
  build, rootfs preparation, formatting, unit tests, and vet passed in the shared VM.
  The real macOS client passed `doctor` and a foreground execution smoke afterward.
- The complete privileged Linux integration suite passed in `casklet-runtime`,
  including the pinned pre-named-network engine from `e470555`. An initial run
  exposed the target-link/transient-unit conflict above; both restart-policy
  regressions and the complete suite passed after fixing the runner.
- Real-VM guard checks verified success, nonzero test exit, SIGTERM, concurrent-run
  refusal, and active-workload refusal. Daily store metadata, image listings, and
  retained Redis/Tomcat records matched before and after testing; the restored
  service uses `/usr/local/bin/casklet` with `Transient=no`.
- The old development VM's home, engine data, and Go installation were archived
  and gzip-validated before deletion. Lima now contains only `casklet-runtime`.
- The full macOS end-to-end suite and Intel/Linux amd64 execution were not run
  for this workflow change.

## 2026-10-10: Independent bounded download-cache management

Added `image cache ls/prune/limit` with focused help and JSON output. Cache-only
cleanup preserves unpacked images, references, and containers; previews report
eligible allocated bytes and blob/staging counts. Stable digest locks remain.
The persistent VM policy defaults to 2 GiB of retained compressed payload, with
zero disabling automatic eviction. Verified reuse updates last-use time through
the pinned descriptor. Policy changes and resolved pull completion evict least
recently used idle blobs, including unchanged pulls, cancellation, and extraction
failure. Active leases are skipped without waiting; they may temporarily exceed
the limit, which excludes download staging and metadata. Pull maintenance uses
a five-second cleanup context. Atomic policy writes and an independent stable
management lock preserve ownership, no-follow, permission, and link-count checks.

`system df` now partitions unpacked `images` and `image-cache` allocation, including
cache directories, locks, and staging in the latter. Pinned no-cross-mount scans
share inode accounting to avoid double counting. Cache reports expose retained
payload and allocated blob bytes separately; reclaimable totals are snapshots.

Regressions cover policy persistence and unsafe artifacts, verified-hit LRU,
protected leases/downloads, independent previews and image survival, unlimited
capacity, success/failure/cancellation maintenance, cancelable locks, CLI bounds,
help parity, JSON results, and partitioned sparse allocation. Privileged CLI
tests exercise cache-only pruning and quota enforcement; real Mac checks preserve
the user's cache by using preview only.

Validation:

- `make fmt-check test vet test-race` passed on macOS arm64 and in the dedicated
  Linux arm64 development VM. The complete image and storage unit suites also
  passed as root. The added mounted-cache partition test passed with a real
  same-filesystem bind mount.
- The complete privileged Linux integration suite passed, including independent
  cache preview/removal, image survival, persisted capacity, automatic post-pull
  eviction, and the pinned legacy supervisor regression.
- `make test-macos` passed through the real client/VM path, including cache status
  and data-preserving previews, nested help parity, cold/warm registry request
  counts, retained containers, and VM stop/start. The product VM's capacity policy
  was left unchanged; cache-only deletion and policy updates ran in the dedicated
  Linux development VM.
- Native and amd64 Darwin client/embedded Linux engine builds passed; the local
  binary was restored to native arm64 afterward. Intel Mac execution and Linux
  amd64 privileged execution were not performed locally.

## 2026-10-09: Image deletion and abandoned recovery outside the metadata lock

Removal checks image leases, retained container references, and mounted storage
under the metadata lock, creates a stable external deletion lock, and atomically
hides the image in a unique `.delete-` tombstone. Recursive cleanup releases the
global lock, honors cancellation, and removes entries through pinned directories
without following links or crossing mounts. The external lock protects partially
removed trees even after their internal usage lease disappears. A replacement
image with the same ID owns a separate directory and lease.

Recovery claims at most 32 abandoned transactions per metadata-lock acquisition
and removes their trees after releasing it, bounding retained file descriptors.
Legacy `.import-`/`.remove-` and abandoned `.prepare-` transactions migrate to the
new deletion protocol. Active external/internal owners remain protected; unsafe
or mounted transactions fail without removal. Older engines ignore the new
deletion prefix. Cancellation and storage failures preserve recovery state.

Deterministic regressions pause live deletion and recovery while unrelated
listing, acquisition, import, and pull succeed. They remove the internal lease
before the pause, republish the deleted image ID, simulate legacy recovery, and
verify cleanup failure/retry, unsafe paths and locks, active legacy leases,
mounted storage, directory batches, and the recovery descriptor bound.

Validation:

- `make fmt-check test vet test-race` passed on macOS arm64 and in the dedicated
  Linux arm64 development VM. The complete image unit suite passed as root,
  including mounted recovery protection and ownership checks.
- The complete privileged Linux integration suite passed with the pinned legacy
  supervisor baseline. Image lease/reference refusal, concurrent creation/removal,
  mounted storage, prune, OCI downloads, and retained image containers passed.
- Native and amd64 Darwin client/embedded Linux engine builds passed; the local
  binary was restored to native arm64 afterward.
- `make test-macos` passed through the real client/VM path, including local
  images, shared download progress, copy-on-write roots, and retained containers.
  VM reboot skipped to preserve the running Redis and Tomcat containers.
- Intel Mac execution and Linux amd64 privileged execution were not performed.

## 2026-10-09: Shared verified image blobs and bounded parallel downloads

Pulls cache compressed SHA-256 blobs in a private `.blobs` directory and download
up to three distinct layers concurrently. Stable per-digest locks serialize
publication and protect active files with shared leases through extraction. Cache
hits recheck safe metadata, declared size, and the entire digest. Repeated manifest
layers reuse one pinned blob without sharing read offsets. Blob publication is
atomic and synced; canceled and failed downloads remove private staging. Errors
cancel and join workers, including pending registry response headers, before image
transaction cleanup. Uncompressed DiffIDs, archive limits, whiteouts, and layer
application order remain unchanged. Progress observers serialize worker events;
warm layers show `Already exists` without fabricated download byte counts.

`image prune` also removes idle compressed blobs and abandoned blob staging,
skipping active download and extraction leases. Preview preserves images/blobs and
continues listing image IDs only. The cache may reuse downloads across image IDs,
but unpacked image roots remain independent. Cached compressed data contributes
to `system df` image storage; it is not part of image identity or required for
running retained containers. Corrupt blobs fail explicitly and can be pruned before
retrying. Stable digest lock files remain after pruning.

Validation:

- `make fmt-check test vet test-race` passed on macOS arm64 and in the dedicated
  Linux arm64 development VM. The complete image unit suite also passed as root.
- Native and amd64 Darwin client/embedded Linux engine builds passed; the local
  binary was restored to native arm64 afterward.
- The complete privileged Linux integration suite passed, including shared-layer
  download counts, cache pruning/redownload, active image leases, and OCI progress.
  OCI progress fixtures now contain a unique archive marker so prior cache data
  cannot turn a cold-download assertion into an unintended warm-cache test.
- `make test-macos` passed through the real client/VM path. Registry GET counters
  verified one cold download per layer and no extra layer downloads for a second
  tag, including host TTY progress. VM reboot skipped to preserve the running
  Redis and Tomcat containers.
- Intel Mac execution and Linux amd64 privileged execution were not performed.

## 2026-10-09: Copy-on-write image roots and concurrent image preparation

New privileged image containers use OverlayFS with an immutable shared lower and
private upper/work directories. Detached storage is atomically published in a
versioned `rootfs.overlay` envelope beside the legacy full `rootfs`. Ordinary
workload files cannot select the backend. Init mounts the merged root only in its
private namespace and prepares image working directories and DNS there. Writes,
whiteouts, renames, read-only execution, exec, and health probes retain their
existing lifecycle semantics. Init inherits the image lease through process exit
to protect the lower after supervisor loss. Directory sources, builtin BusyBox,
user namespaces, rootless execution, and existing complete copied roots keep the
copy path; existing roots are not migrated. Unsupported OverlayFS mounts fail
explicitly. Acquisition still verifies all image content.

Image downloads, extraction, directory copying, hashing, and syncing now run in
private leased transactions outside the global metadata lock. Stable reference
locks serialize refreshes of one name before registry resolution. Lookup and
publication acquire image leases under the global lock, then verify content
outside it. Stable external transaction locks protect recursive staging cleanup
even after internal lease files disappear. The new `.prepare-` prefix prevents
older engines' recovery from reclaiming live new transactions. Abandoned staging
recovery and image removal still use the metadata lock; this change does not add
a shared download-layer cache.

Regressions cover sparse storage and ownership, unsafe or mismatched backend
metadata, isolation, whiteouts/rename across restart, retained lower references,
supervisor-loss leases, and legacy copied roots with unavailable image sources.
Image tests pause a download while cached operations, imports/removal, and other
pulls complete, and cover ordered/cancelable refreshes, concurrent identical
publication, cancellation rollback, live/abandoned transaction recovery, legacy
recovery compatibility, and corrupt deduplication targets. The real Mac CoW test
also checks health probes, restart, and a 4 MiB unchanged lower fixture.

The full Linux run exposed a recovery timing gap after supervisor death: systemd
could report a stopped service while its run lock or populated cgroup remained
briefly visible. Targeted recovery now retries only these busy conditions for at
most two seconds, honors caller cancellation, and treats concurrent removal of
the run directory as completion. Each attempt still validates the path, acquires
the lock, and checks the cgroup; a timeout preserves the resources. Deterministic
tests cover competing recovery, held locks, and cgroup drain/preservation.

Validation:

- `make fmt-check test vet test-race` passed on macOS arm64 and in the dedicated
  Linux arm64 development VM. Root-only image ownership and rootfs tests passed.
- Native and amd64 Darwin client/embedded Linux engine builds passed; the local
  binary was restored to native arm64 afterward.
- The complete privileged Linux integration suite passed, including all four
  new overlay tests and the pinned pre-named-network supervisor regression using
  baseline commit `e470555`. The supervisor-recovery test that initially exposed
  the timing gap passed ten consecutive runs after the bounded retry fix, then
  passed in the full suite. No cleanup or lease assertion was weakened.
- `make test-macos` passed through the real client/VM path after the final runtime
  changes. The CoW fixture retained only 28 KiB of writable storage while sharing
  an unchanged 4 MiB lower file. Engine/template repair, health, networks, volumes,
  restart policies, and terminals passed. VM reboot skipped to preserve other
  running Redis and Tomcat containers.
- Intel Mac execution and Linux amd64 privileged execution were not performed.

## 2026-10-09: Wait for service readiness

Added `wait --healthy --timeout DURATION` using existing saved health state.
The default deadline is 30 seconds; zero disables it. Resolution pins the full ID
and generation, and the cancelable deadline covers storage recovery, initial
metadata lookup, and sampling. Starting or unhealthy checks may recover; stop,
exit, removal, or an execution change fails. Missing or disabled checks fail
immediately. Success prints zero; a readiness timeout returns 124 without changing
the workload. Plain exit waits retain their existing receipt behavior, and timeout
is accepted only with the health flag. Focused help, usage, architecture, and both
READMEs describe these semantics.

Regressions cover early success, pending startup without published health,
unhealthy recovery, stale healthy state after stop/exit, immutable identity and
generation, bounded metadata reads, caller cancellation/deadline distinction,
argument validation, help parity, and timeout status mapping. Bounded Linux and
real Mac tests cover delayed readiness, timeout without stopping the workload,
missing checks, stopped containers, and cancellation through their transports.
Darwin arm64 build, full unit tests, vet, formatting, and container/CLI race tests
passed. Linux arm64 full unit tests, vet, formatting, and container/CLI race tests
passed in the dedicated development VM. Privileged readiness, health-probe, and
existing exit-wait regressions passed, as did real Mac readiness, cancellation,
health/restart, and exit-wait/listing checks. The complete privileged and macOS
suites and Intel execution were not run for this step.

## 2026-10-09: Container labels, filtering, and batch lifecycle operations

Detached `run --label KEY=VALUE` and JSON `label` arrays save bounded immutable
metadata independently of environment values. Last repeated keys win, including
CLI overrides of config labels; labels survive restarts and appear in ps JSON
and inspect. Store updates and completion reject label mutation. Repeated ps,
stop, and rm filters intersect label presence/value, lifecycle status, and health
predicates, including repeated predicates of the same type.

Stop/removal accept multiple references or explicit filter/all selection. Batches
snapshot full IDs before mutation, deduplicate, preserve operand order, and sort
selector results. Each operation uses the existing operation lock and current
state checks; running removal remains forbidden. Successful IDs remain on stdout
while every failure is reported and aggregated to a nonzero status. Cancellation
and output failures stop further work; empty selections succeed. Selection never
redirects a reused name or includes containers created later.

Regression coverage includes literal/empty values, bounds and escaped JSON,
configuration precedence, persisted labels across reopening/executions, immutable
updates/completion, filter intersection and health derivation, snapshot ordering,
deduplication, partial failures, cancellation, output errors, and focused help.
Scoped macOS and privileged Linux lifecycle tests plus a deterministic Linux
name-reuse/operation-lock race are added. Formatting, full Darwin unit tests,
vet, Darwin arm64 client/Linux engine builds, and relevant Darwin race tests
passed. Linux arm64 full unit tests, vet, formatting, and container/config/CLI
race tests passed in the dedicated development VM. Privileged label/filter/batch
lifecycle and name-reuse/operation-lock regressions passed, together with readiness
regressions. Real Mac label/filter/batch, short-ID lifecycle, and readiness checks
passed. The complete privileged and macOS suites and Intel execution were not run
for this step.

## 2026-10-09: Unique short IDs for container and image operations

Container lookup now accepts unique hexadecimal ID prefixes of any length,
after full-ID and exact-name lookup. All management commands reuse that resolver.
Ambiguous prefixes return a distinct error instead of selecting a container;
removal pins the resolved full ID before locking and deleting storage.

Local image resolution, acquisition, and removal accept 1–64 hexadecimal digits
with an optional sha256: prefix. Acquisition and removal resolve under the store
lock and use full IDs for leases and retained-container reference checks. Cached
repository names take precedence over bare prefixes for run --image; missing
local IDs and ambiguous prefixes never trigger automatic pulls. Persisted IDs,
JSON, and lifecycle stdout remain complete. This supersedes the display-only
short-ID restriction recorded below. Help, examples, and bilingual documentation
describe unique matching, ambiguity, and the remaining ID-only image removal API.

Regressions cover prefix lengths, name precedence, deterministic collisions,
missing IDs, invalid paths, immutable resolution, live leases, retained references,
and short-ID execution/removal. Darwin arm64 build, full Darwin/Linux unit suites,
vet, formatting, and relevant Linux race tests passed. Real Mac help/argument and
short-ID lifecycle tests passed. The complete privileged Linux suite passed all
cases except an old image-table assertion expecting full displayed IDs; after
updating it for the established 12-character table, that test passed on rerun.
The new privileged short-ID lifecycle test passed in that suite. The complete
macOS suite and Intel execution were not run.

## 2026-10-09: Consistent container ID presentation

Container IDs in ps and stats tables now use the same 12-character presentation
as image listings through a shared display helper. JSON, inspect, and lifecycle
stdout retain full IDs. Short IDs remain display-only; help, lookup diagnostics,
and bilingual usage documentation point to exact names or full IDs from ps JSON.

Existing container table and statistics regressions now verify truncation of real
32-character IDs and full-ID JSON preservation; image formatting and focused help
checks also pass. Darwin arm64 build, full unit tests, vet, formatting, and real
Mac ps/stats table and JSON checks passed with the existing Redis/Tomcat containers.
Runtime behavior was unchanged; privileged Linux integration tests and the full
macOS end-to-end suite were not run.

## 2026-10-09: Readable image listings

The image table now starts with IMAGE and shows one row per cached reference,
omitting Docker Hub's default registry and official library prefix. References
from other registries retain their registry and namespace; unreferenced images
show <none>. IDs display 12 hexadecimal characters, and unpacked file sizes use
decimal B/kB/MB/GB/TB/PB/EB units rounded to three significant digits. JSON retains
full IDs, canonical references, and exact byte counts. Help and bilingual usage
documentation direct removal and ID-based execution to the full IDs from JSON.

Regressions cover multiple references, registry ports, namespaces, digest
references, unreferenced images, empty tables/JSON, output failures, unit boundaries,
rounding, large sizes, JSON preservation, and focused help. Darwin arm64 build,
full unit tests, vet, formatting, and real Mac image table/JSON checks passed
against cached Redis and Tomcat images. Runtime behavior was unchanged; privileged
Linux integration tests and the full macOS end-to-end suite were not run.

## 2026-10-09: Detached container health and image HEALTHCHECK support

Added independent starting/healthy/unhealthy/stopped health state to ps/inspect,
with five bounded probe results and no probe output. Image launch defaults retain
the Docker Healthcheck extension, exec/shell/NONE tests, custom shells, all timing
fields including raw StartInterval, and identity coverage. Explicit flags and
JSON configuration merge field overrides. Explicit pulls refresh newly understood
launch metadata even when the source manifest has not changed, preserving old
immutable IDs and retained configurations.

One supervisor-owned monitor uses pinned container exec resources and a child
cgroup, with immediate timeout/cancellation killing and no overlapping probes.
Probe identity, environment, workdir, mounts, security, and resource limits match
the workload. Grace-period failures, early success, retries, recovery, metadata
contention, stop state, restart resets, and generation-fenced results preserve
lifecycle semantics. Health failure does not trigger restart policies. Foreground
runs do not schedule health checks. Documentation and focused help describe these
boundaries, migration, timing limits, and readiness separately from process startup.

Validation across all three additions: Darwin arm64 build, formatting, complete
unit/race suites, and vet passed; Linux arm64 unit/race suites, vet, and the full
privileged integration suite passed. The full real macOS suite passed, including
configuration/env-files, binary volume archives, health transitions, and probe
cleanup. The VM reboot test skipped to preserve other active workloads. Final image
metadata upgrade, OCI health, and help regressions passed after the cache-compatibility
adjustment. Intel execution was not run.

## 2026-10-08: Atomic named-volume backups and restores

Added stdout tar export and stdin restore, including transport through the Mac
client. Export requires an exclusive usage lease and rejects mounted/special
files; stopped retained references are allowed. Restore creates a new name using
leased private staging, validates paths/types/links and complete tar termination,
fsyncs data and metadata, and rechecks the name before atomic publication.
Concurrent operations skip a live restore's lease; failed and abandoned staging
is reclaimed without replacing an existing volume. Streams preserve numeric
ownership, permissions, modification times, symlinks, hardlinks, and file bytes.
Limits, exclusions, consistency, migration, and shell failure handling are documented.

Validation: Darwin arm64 build, unit tests, vet, and formatting passed. Linux
arm64 volume/CLI race tests and vet passed. Privileged volume regressions passed,
including numeric ownership, retained references, active leases, and mounted-tree
refusal. Real macOS tar transport/restore/readonly reuse passed. Intel execution
was not run.

## 2026-10-08: Repeatable run configuration and environment files

Added strict JSON `run --config` and repeatable `run/exec --env-file` options.
Local expansion precedes VM initialization, privilege delegation, and transport;
retained executions save merged values. CLI scalars and explicit commands override
config defaults; repeated options append. Relative file paths use the config's
directory, and all explicit env values override env-files. Bounded regular-file
reads, duplicate/unknown-key rejection, literal values, CRLF, and sanitized env-file
errors are covered by regressions. Focused help and bilingual quick starts agree.

Validation: Darwin arm64 build, unit tests, vet, and formatting passed. The real
macOS configuration/env-file test passed, including CLI overrides, exec, and restart
after deleting the local files. Linux runtime behavior is unchanged by this step.

## 2026-10-08: Durable automatic restart policies

Detached containers now accept `--restart no|on-failure[:1-1000]|always|unless-stopped`.
The boot-enabled guest manager uses existing operation locks, retained roots,
resource cleanup, and generation receipts. Four workers poll fairly; durable
exponential backoff caps at 30 seconds. Preparation failures consume retries,
healthy executions reset consecutive attempts, and manual stop suppresses retries.
Boot identities preserve `unless-stopped` choices while allowing `always` to
resume. Engine installation and replacement manage the daemon independently of
workload supervisors. Inspection exposes policy, counter, pending deadline, and
manual-stop state; wait stays attached to its original generation.

Concurrent inspection exposed a loaded-but-not-yet-started systemd unit window.
Scheduling grace now protects that window without hiding completed supervisors.
Validation: Darwin arm64 build, formatting, unit tests, and vet passed. Linux arm64
unit/vet and relevant race checks passed. The full privileged Linux integration
suite and full real macOS suite passed; six-container concurrent restarts also
passed eight consecutive runs. The product VM reboot test skipped to preserve
other active workloads; a real dedicated development VM reboot verified all four
policies, manual-stop persistence, and retained data using a persistently installed
engine. Automatic guest restarts cannot preflight Mac socket
ownership; asynchronous forwarding remains Lima's responsibility. Intel execution
was not run.

## 2026-10-08: OCI and explicit managed stop signals

Image startup defaults now retain and validate `StopSignal`; `run --stop-signal`
overrides it with a Linux signal name or number. Managed stop requests send the
selected signal to the workload process group without changing the supervisor's
control signal. Retained configurations and inspection preserve the selection.
External foreground signals, timeout, orphan handling, and descendant cleanup
keep their existing semantics and forced SIGKILL boundaries. Empty legacy image
fields preserve their previous identity serialization.

Validation: Darwin arm64 build, formatting, unit tests, and vet passed. Linux
arm64 unit/vet and config/image/runtime/container race checks passed. Privileged
OCI defaults/offline overrides, custom named/numeric stop signals, restart,
shutdown deadlines, external signals, and descendant cleanup tests passed. The
real macOS stop-signal/inspection/wait regression passed. Intel execution was not run.

## 2026-10-08: Guest disk usage and safe image pruning

Added `system df [--json]` with guest capacity/free/available bytes, low-space status,
and allocated-block totals for images, containers/logs, volumes, runs, and templates.
Scans pin directories, never follow symlinks, skip mounted descendants (including
same-filesystem bind mounts), and deduplicate hardlinks within each category.
Added `image prune [--dry-run]`; candidates are rechecked through the existing
exclusive image lease and retained-container reference protocol. Partial failures
preserve completed IDs for presentation. No container, log, or volume is pruned.

Validation: Darwin arm64 build, formatting, unit tests, and vet passed. Linux arm64
unit tests/vet and storage/image race tests passed. Privileged disk-accounting,
bind-mount exclusion, pruning, and all image integration tests passed. Real macOS
disk reporting and non-destructive prune preview passed alongside volume regression.
The Mac VM disk file's physical compaction is outside this feature's scope.

## 2026-10-08: Guest-local named data volumes

Added explicit `volume create/ls/inspect/rm` commands and named/readonly mounts.
Volumes start empty on the VM disk, survive container removal, and are protected
by shared runtime leases plus references from every retained container. Init
inherits close-on-exec leases so supervisor loss cannot immediately authorize
volume deletion. Private metadata, no-follow paths, atomic staging/publication,
and mounted-tree refusal protect creation and removal. Existing bind mounts and
Mac path translation remain compatible. User namespaces are explicitly excluded.

Validation: Darwin arm64 build, formatting, unit tests, vet, and relevant race tests
passed. Linux arm64 unit tests, vet, volume/config/container/runtime race tests,
and the full privileged integration suite passed, including foreground leases,
retained references, readonly reuse, and replacement-container data retention.
The real macOS named-volume end-to-end test passed. Intel execution was not run.

## 2026-10-08: Synchronize bilingual README application workflows

The English and Chinese READMEs now cover the same OCI application and progress
workflows, image cache/refresh/removal, interactive exec, retained data, VM storage
paths, build prerequisites, and client/engine upgrades. The Chinese README's stale
mandatory-separator statement is corrected: image defaults need neither a command
nor `--`. The usage guide makes that condition explicit too. Language navigation
and corresponding shell examples are aligned. Repository guidelines require
bilingual README updates and focused help/documentation updates for CLI changes.

Validation: both READMEs have five corresponding sections and eight matching shell
example blocks (ignoring translated comments). Shell syntax, local Markdown links
and anchors, shared feature details, and diff whitespace checks passed. This change
only updates documentation; runtime and application tests were not repeated.

## 2026-10-08: OCI pull progress through the macOS client

Automatic image preparation and explicit image pulls now expose operation-scoped
progress events. Download bytes count the compressed stream against manifest layer
sizes without additional downloads; archive reads report extraction progress.
Digest checks, atomic publication, cancellation, and image leases retain their
existing behavior. Cached runs and unchanged explicit pulls have distinct status
messages. Failed and canceled layers do not emit completion, and output errors
stop further presentation without changing the image transaction.

The CLI renders progress on stderr and preserves exact stdout IDs/workload output.
Automatic display mode is selected on the Mac before SSH dispatch: terminals redraw
up to six recent layer rows, while pipes/files receive throttled text without ANSI
controls. Both commands accept `--progress=auto|plain|tty`. No extra guest PTY is
allocated for progress, preserving detached command stream separation.

Validation: Darwin and Linux arm64 unit/race tests and vet passed; formatting and
diff checks passed. Linux OCI/generic-root privileged integration tests passed,
including automatic/explicit pulls, exact streams, cache status, and failed refresh.
The full macOS end-to-end suite passed, including an in-VM local registry test with
plain output and a host stderr PTY (the VM reboot case skipped to preserve another
active container). Linux and Darwin amd64 cross-builds passed. Tests also cover
compressed versus unpacked byte counts, throttling/final updates, unknown totals,
output failure, option forwarding, and cancellation cleanup that preserves cache.

## 2026-10-08: OCI application images and generic filesystems

Registry names now resolve to cached immutable images or pull the native Linux
platform through go-containerregistry without Docker Engine. Explicit image pulls
refresh tags. Downloaded layer digests and DiffIDs are verified, whiteouts precede
same-layer additions, and root-confined extraction supports merged `/usr`, symlinks,
and hardlinks. Interrupted or failed pulls preserve the prior reference and leave
no published partial image. Local identities cover ownership and startup defaults;
leases protect source resolution, creation, execution, and deletion.

Image entrypoints, commands, environment, working directories, and numeric/named
users are merged with explicit options before container creation. Retained containers
save the immutable ID and merged execution config. Generic filesystems require no
BusyBox; the managed builtin still receives its original strict health validation.
Runtime copies preserve image UID/GID, create missing working directories before
read-only setup, and provide private shared memory. OCI root workloads receive only
the bounded capabilities needed for initialization, user switching, capability
reduction with setpriv, and HTTP listeners. Directory and non-root policies still
drop all capabilities. User namespaces remain unsupported for OCI sources.

Regression coverage includes multi-platform local registries, layer replacement,
whiteouts/opaque directories, archive traversal, host-target symlinks, forward and
cross-layer hardlinks, duplicate paths, unsupported devices, digest/size bounds,
cancellation, cache refresh/offline use, config integrity, account lookup, default
and override precedence, ownership across copies, retained restart, image leases,
capability dropping before user switching, and a generic root with no BusyBox or
runtime mount targets. Existing builtin repair tests continue to pass.

Validation: Darwin and Linux arm64 unit/race tests and vet passed, as did the full
privileged Linux integration suite, additional OCI/generic-root tests, and the full
macOS end-to-end suite (the VM reboot case skipped to preserve another active
container). Formatting and diff checks passed. Vulnerability checks
reported no reachable vulnerabilities for Darwin/Linux on arm64/amd64. Real macOS
smokes pulled Redis 8, Nginx stable, and PostgreSQL 17: Redis answered host TCP PING
and retained a key across restart, Nginx served its HTTP page on the Mac, and
PostgreSQL initialized and executed a SQL query with a configured password and
512 MiB memory limit. Temporary smoke containers were stopped and removed.

## 2026-10-08: Allocate log snapshots once

Log snapshots allocate one buffer sized to validated unread bytes and read each
retained segment directly into it. This removes growing per-segment buffers and
concatenation copies. Per-container locking, legacy writer compatibility,
retention bounds, inode pinning, generation fencing, and follower offsets remain
in place. Cancellation is checked before allocation and between segment reads.

`TestLargeLogSnapshotPreservesSegmentsAndCursorSuffix` verifies multi-buffer
segments, chronological content, unchanged snapshots, and appended suffixes.
Existing rotation, retention-overrun, unsafe-artifact, legacy, lock contention,
and concurrent snapshot tests passed. `BenchmarkLogSnapshot` reads four 4 MiB
segments: Linux arm64 allocation fell from about 87.26 MB to 16.78 MB per
snapshot (81%). Three ten-iteration samples were run before and after; this
measures allocations for the fixture rather than total process memory.

Final cumulative validation for engine integrity and the two allocation
optimizations: `make build fmt-check test vet test-race` passed on macOS arm64;
formatting, unit tests, vet, race checks, and the complete privileged integration
suite passed in the Linux arm64 VM. `make test-macos` passed, including VM
stop/start, corruption repair, local images, log rotation/follow cancellation,
signals, lifecycle ports, and interactive terminals. The final fault-test cleanup
adjustment passed its macOS end-to-end test and vet again. Intel Mac and Linux
amd64 execution were not run locally.

## 2026-10-08: Reuse image identity read buffers

Image identity hashing uses one 32 KiB buffer per traversal across regular
files. Concurrent traversals own separate buffers. The version-1 digest format,
sorted traversal, content/mode checks, and cancellation checks are preserved.

`TestIdentityPreservesPersistedDigestAcrossFileSizes` pins the pre-optimization
digest for empty, small, and multi-buffer files with explicit permissions.
Existing identity, import integrity, lease, rollback, and concurrent image
tests also passed. `BenchmarkIdentitySmallFiles` measures 256 files of 1 KiB:
Linux arm64 allocations fell from about 8.72 MB to 0.35 MB per traversal (96%);
macOS arm64 showed the same allocation reduction. Three ten-iteration samples
were run before and after; elapsed timings are environment-dependent.

Validation: formatting and relevant unit, vet, and race checks passed on macOS
and Linux arm64. Seven privileged Linux image tests and the macOS local-image
and live-bind end-to-end test passed.

## 2026-10-08: Verify installed engine content before reuse

The engine reuse check verifies actual executable content against the bundled
SHA-256 in addition to the installation marker and executable permissions.
A damaged executable with an unchanged marker is reinstalled atomically before
it can handle guest operations. Existing supervisors keep their pinned binary.

Regressions: `TestInstallationCheckRequiresMatchingEngineAndMarker` includes
corrupt executable content with the correct marker. The macOS
`TestMachineRepairsCorruptExecutableEngine` replaces the installed engine with
an executable failure script, checks automatic repair and a surviving running
container, and confirms healthy installations are reused. Both tests reproduced
the gap before the fix.
Fault-test cleanup restores the engine before stopping and removing its
container, so a failed repair assertion can still release the test workload.

Validation: `make build fmt-check test vet` passed on macOS arm64. The guest
check's unit tests and vet passed in the Linux arm64 VM. macOS end-to-end tests
for corrupt and nonexecutable engine repair passed.

## 2026-10-08: Exec cleanup error exit status

The foreground infrastructure-error policy also applies to `exec`: a successful
command followed by cleanup failure returns 125. Nonzero command statuses are
preserved, and cleanup diagnostics are written to stderr.

Regressions: `TestExecCleanupFailureReturnsNonzeroAndPreservesCommandStatus`
in both privileged Linux and macOS end-to-end suites injects an unrecognized
child into the session's actual cgroup. It checks exit 0 becomes 125, exit 7
stays 7, stderr identifies the failed removal, and the main container retains
its running generation and accepts another exec. The tests reproduced the
incorrect zero status on both platforms before the fix.

Final cumulative validation for the three 2026-10-08 fixes: `make build` and
`casklet doctor` passed. `make fmt-check test vet test-race` passed on macOS
arm64 and in the dedicated Linux arm64 VM. The complete privileged Linux
integration suite and `make test-macos` passed, including VM stop/start,
engine/template repair, signals, lifecycle ports, exec cleanup, and interactive
terminals. Intel Mac and Linux amd64 execution were not run.

## 2026-10-08: Retained-container host port preflight

Mac `start` and `restart` check the saved published ports before creating a new
execution. A private bounded handshake keeps the guest operation lock across
stop, host authorization, and startup. Rejection, disconnect, and cancellation
preserve the stopped generation. Running `start` remains idempotent. Checks
briefly wait for asynchronous Lima socket release and probe local IPv4
addresses for wildcard bindings, which BSD can otherwise allow alongside an
existing address-specific TCP listener. Lima's final bind remains asynchronous
and cannot be reserved atomically by preflight.

Regressions: `TestHostLifecyclePreflightFencesGenerationAndOperations` checks
both lifecycle actions, lock contention, denial, disconnect, signal cancellation,
authorization, and retry. `TestLifecycleRejectsOccupiedMacPorts` covers TCP/UDP,
wildcard/localhost, unchanged rejected generations, retry, idempotent start,
and active restart. Unit tests cover bounded protocol decoding, guest-to-host
address translation, fragmented output, malformed messages, port release,
cancellation, and TCP/UDP conflicts including wildcard shadowing.

Validation: formatting, unit tests, and vet passed on macOS arm64 and in the
dedicated Linux arm64 VM. Relevant macOS race tests and Linux race checks
passed. The complete privileged Linux suite and targeted macOS lifecycle and
TCP/UDP publishing tests passed. Intel Mac and Linux amd64 execution were not
run.

## 2026-10-08: Nonterminal signal cleanup

Foreground supervisors and namespace init processes handle HUP and QUIT for
both terminal and nonterminal runs. Signals cancel preparation or reach the
workload through the existing bounded shutdown path. The macOS watchdog's HUP
now performs normal resource cleanup after abrupt client loss.

Regressions: `TestSignals` checks all four forwarded signals, trap exit status,
and bridge/NAT cleanup; `TestSignalWhileWaitingForStateLock` checks cancellation
before startup for all four signals. `TestNonTTYHangupAndQuitCleanup` verifies
the macOS transport, and `TestAbruptClientLossCleansForegroundSession` now checks
the invocation's run directory, cgroup, and NAT table as well as its processes.

Validation: formatting, unit tests, and vet passed on macOS arm64 and in the
dedicated Linux arm64 VM. Linux race checks and the complete privileged
integration suite passed. Targeted macOS signal, watchdog, and noninteractive
TTY end-to-end tests passed. Intel Mac and Linux amd64 execution were not run.

## 2026-10-07: Capture, cleanup, and transaction recovery

### Recover log capture after temporary contention

Rotation and snapshots use a per-container log lock. Lock acquisition briefly
uses the metadata lock; waiting and I/O release it. Removal respects that log
lock. Older records acquire a validated lock file lazily. Active supervisors
from an older engine retain their metadata-lock protocol until completion or
restart, and new readers honor it during an engine upgrade.

A lock wait exceeding five seconds drops only its current chunk, marks the log
truncated, and records a contention diagnostic. Later output is saved after
contention clears. Permanent storage failures still drain output to keep the
workload from blocking indefinitely.

Regressions: `TestLogCaptureResumesAfterLockTimeout`,
`TestCaptureLogResumesAfterTemporaryLockFailure`,
`TestLogLockDoesNotBlockMetadataOrOtherContainers`, and
`TestLogLockUpgradesOldRecordsAndRejectsUnsafeFiles`, plus
`TestLegacyRunningLogUsesMetadataLockUntilCompletion`. Lock timeout recovery
covers both log and metadata lock contention.

### Persist cleanup failures and retain recovery receipts

Runtime returns typed cleanup failures separately from command status. Failed
cgroup emptying or removal keeps its run directory and cgroup receipt. Detached
records, completion receipts, and public inspection retain sanitized failure
stages. Detailed errors remain in private state and logs. Restart clears current
failures and preserves the previous execution's failures. Foreground CLI calls
return 125 for infrastructure failure when the command itself returned zero.

Regressions: `TestWorkloadCleanupRecordsFailuresAndPreservesRecoveryState`,
`TestCleanupStagesPreservesJoinedErrorsWithoutPrivateDetails`,
`TestCleanupFailuresSurviveRestartInExecutionReceipt`,
`TestCompletionRecoversAlreadyPublishedReceipt`, and the privileged
`TestCleanupFailureRetainsExitStatusAndRecoveryReceipt`.

### Recover abandoned container storage transactions

Store opening, creation, listing, and removal recover verified `.create-ID` and
`.remove-ID` directories. Recovery validates ownership, permissions, existing
leases, and mounted trees. A stable external transaction lock protects live
deletion even after internal lease files are gone. Recursive deletion releases
the metadata lock. Other commands skip recovery while deletion is active.

Regressions: `TestStoreOperationsRecoverInterruptedTransactions`,
`TestRecoveryPreservesLeasedTransactions`,
`TestLiveDeletionDoesNotBlockMetadataLogsOrRecoverItsPartialTree`,
`TestTransactionRecoveryRejectsUnsafeArtifacts`,
`TestTransactionRecoveryHonorsCancellation`, and the privileged
`TestContainerTransactionRecoveryPreservesMountedTrees`.

### Validation

- `make fmt-check test vet test-race` passed on macOS arm64 and in the dedicated
  Linux arm64 development VM.
- `make build` and `casklet doctor` passed with the native Darwin client and its
  bundled Linux engine.
- The complete privileged Linux integration suite passed, including cleanup
  failure persistence and mounted transaction preservation. Final compatibility
  adjustments additionally passed container/runtime race tests and seven
  targeted privileged tests for logs, cleanup, transactions, and concurrent
  lifecycle operations. Cleanup failure coverage preserves both exit 0 and 7.
- Eighteen macOS end-to-end tests passed, covering streams, logs, lifecycle,
  images, binds, rootless execution, published ports, signals, inspection, and
  terminals. After the final compatibility adjustments, lifecycle, wait,
  inspection, and interactive run/exec tests passed again.
- Intel Mac execution, Linux amd64 privileged tests, VM stop/start, and the
  macOS engine/template corruption tests were not run in this change.

## 2026-10-09: Isolated named networks and service discovery

Added `network create/ls/inspect/rm`, `run --network NAME`, and detached
`--network-alias`. Named metadata uses atomic publication and stable deletion
leases; all retained members and unrecovered allocations block removal. Names
and aliases reserve one container per network even when stopped. Each named
network uses a separate bridge subnet, with forwarding to other casklet networks
blocked. Supervisor-owned UDP/TCP resolvers provide fresh scoped IPv4 answers,
exclude abandoned/previous-boot executions, and preserve upstream DNS. Queries,
connections, and shutdown are bounded; listening ports remain reserved through
NAT cleanup. Separate journals preserve strict old-supervisor bridge parsing,
and forwarding receipts protect mixed engine generations. Failure deleting the
last bridge preserves its allocation for retry.

DNS regressions cover UDP/TCP, changed addresses, missing/local names, unsupported
record types, malformed input, source restriction, bounded workers, and immediate
shutdown of slow clients. The combined macOS project regression checks discovery
after an address change, health probes through scoped DNS, readiness waiting,
host publication, and filtered batch cleanup together.

Validation:

- `make fmt-check test vet test-race` passed on macOS arm64 and the dedicated
  Linux arm64 VM. Native and amd64 Darwin/embedded Linux builds passed.
- Root-only network metadata, lease, cancellation, canonical subnet, unsafe
  artifact, and abandoned-execution DNS tests passed.
- The complete privileged Linux integration suite passed, including named
  discovery/isolation, UDP/TCP upstream DNS, cleanup failure receipts, forwarding
  restoration, and a pinned pre-named-network supervisor. The pinned-supervisor
  regression additionally passed three consecutive runs.
- `make test-macos` passed through the real client/VM path, including the combined
  project regression. The VM stop/start test skipped to preserve other running
  containers. Engine/template repair tests passed.
- The first Linux suite run observed one empty systemd service cgroup retained
  after supervisor-death recovery; its workload descendants were gone. Three
  consecutive targeted retries and the complete suite rerun passed. That initial
  empty parent directory remained; no cleanup assertion was weakened. The same
  recovery test also passed 30 runs with the pre-named-network engine.
- Intel Mac execution and Linux amd64 privileged execution were not performed.
