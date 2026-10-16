# Architecture

[Quick start](README.md) · [Usage](guides/usage.md) · [Development](guides/development.md)

casklet is a macOS host application with an embedded Linux guest engine.
It separates VM management and host transport from command handling, durable
container management, runtime supervision, and Linux resource adapters. The public CLI and versioned container
records remain stable while these internal responsibilities evolve.

## macOS host and Linux guest

The public Darwin build embeds a native-architecture Linux build of the same
engine entry point. The Darwin CLI validates arguments, supplies the built-in
BusyBox default, and dispatches to `internal/machine`; the Linux CLI continues
to execute inside the guest. Linux is an internal component, not a supported
public host installation target.

```mermaid
flowchart LR
    Mac[macOS CLI] --> Machine[machine: VM lifecycle and path translation]
    Machine --> Lima[Lima: VZ VM and virtiofs / port forwarding]
    Machine --> SSH[SSH: streams and terminal]
    SSH --> Remote[guest remote: signal relay]
    Remote --> Engine[existing Linux CLI and engine]
```

`machine` serializes creation, startup, and engine installation using a host
file lock. It owns the `casklet-runtime` instance configuration and embeds
the engine plus the rootfs preparation helper. The payload hash determines
whether an engine update is needed. Installation atomically replaces the guest
executable and preserves the Linux container/image stores. The writable Mac
home share provides installation staging and live bind sources; persistent
container roots remain on the Linux disk. Development and tests reuse this same
VM, with optional guest build tools and source snapshots on its Linux disk. The
privileged test runner requires exclusive VM use, temporarily saves the daily
engine store and installed restart manager, and restores both after testing.
The reuse check verifies the installed executable's SHA-256 as well as the
version marker, so executable corruption triggers atomic reinstallation before
guest engine execution.

VM running state and guest provisioning readiness are independent. The host
checks the completion marker and required dependencies before engine setup;
incomplete setup uses the same embedded shell script as Lima's initial provision.
A guest flock serializes both paths, package retries/lock waits and a guest
timeout bound failures, and only successful setup publishes the completion
marker. Repair runs in place, preserving engine storage and already delegated
user-manager executions. Preview rejects incomplete provisioning with a start
hint instead of repairing it.

Engine caching is independent of builtin template health. A private guest
installation operation checks the template's layout, applet links, permissions,
architecture, and recorded BusyBox checksum. Repairs generate and validate a
private sibling tree before a Linux rename exchange publishes it atomically.
The template module owns a stable lease outside the exchanged directory: source
copies hold it shared, and repair holds it exclusively. Runtime releases the
copy lease once its private root is ready, so running workloads do not delay
repair. Interrupted staging trees are reclaimed under the exclusive lease;
symlinks, unsafe directory ownership/permissions, and mounts block reclamation.

SSH carries exact shell-quoted argv, standard streams, exit status, and PTYs.
`remote` owns per-session Unix datagram sockets in private guest directories,
relays control signals to the CLI child, and removes sockets when sessions end.
A small macOS watchdog inherits a private lifeline pipe. Normal completion
sends a marker; abrupt client death closes the pipe without that marker, and
the watchdog sends a hangup to the guest session. SSH keepalives bound broken
connections. Guest parsing supplies guest UID/GID defaults for rootless commands.
Isolation re-execution modes remain guest-local; the watchdog is host-local.

Published Mac wildcard and localhost addresses translate to dedicated guest
loopback addresses. Lima forwards only those addresses using its gRPC forwarder
for TCP and UDP; the Linux network adapter continues to own NAT and port leases.
Lima owns host forwarding cleanup. The host checks occupied ports before launch,
but cannot reserve Lima's eventual socket atomically.
For retained containers, a private lifecycle handshake checks saved published
ports after the guest stops the old execution. The guest holds the immutable
container's operation lock throughout stopping, host authorization, and startup.
The host briefly waits for Lima to release old sockets, checking wildcard ports
against local IPv4 addresses too. Refusal, disconnection, or cancellation leaves
the stopped generation intact. An already running `start` needs no check.

## Module boundaries

```mermaid
flowchart TD
    main[cmd/casklet] --> cli[cli: parse, launch, dispatch, output]
    main --> container[container: lifecycle and durable state]
    main --> runtime[runtime: supervisor and namespace children]
    cli --> container
    cli --> runtime
    cli --> image[image: OCI registry and immutable snapshots]
    container --> runtime
    container --> template[template: source resolution and leases]
    runtime --> template
    template --> image
    template --> rootfs[rootfs: validation, copying and mounts]
    image --> rootfs
    runtime --> rootfs
    runtime --> cgroup[cgroup: limits and cleanup]
    runtime --> network[network: bridge, NAT and recovery]
    runtime --> ipc[ipc: bounded messages and descriptor transfer]
```

The diagram shows the main startup dependencies; `config` supplies shared
execution data and validation. Container inspection also reads cgroup metrics,
while managed exec uses runtime resources and IPC.

| Package | Owns | Keep out |
| --- | --- | --- |
| `cmd/casklet` | Process entry and private re-exec modes | Parsing and lifecycle policy |
| `cli` | Flags, privilege/delegation launch, signal contexts, presentation | Persistent state and Linux isolation |
| `project` | Strict deployment graph, scoped resource names, effective-config identity, cancelable local project lock and resource receipts, bounded startup/rollback orchestration | Namespace setup, registry transport and guest lifecycle implementation |
| `config` | Serializable execution values and validation | CLI output and resource allocation |
| `container` | Records, generations, operation locks, systemd services, logs, health monitoring, inspection | Namespace setup and workload execution |
| `runtime` | Startup handshake, PID 1, signals, exec sessions, run recovery and cleanup | User-facing command parsing and container records |
| `template` | Directory/image selection, canonical validation, image lease ownership | Copies, mounts, and lifecycle state |
| `registryauth` | Canonical registry scopes, native macOS Keychain entries, bounded credential envelopes, redirect authorization policy | Container configuration and durable guest secrets |
| `image` | Registry resolution, platform selection, layer application, startup defaults, content identity, atomic publication and deletion leases | Runtime supervision |
| `volume` | Private volume metadata, usage leases, tar streams, staged restore publication | Container lifecycle and host paths |
| `rootfs` | Confined filesystem access, copying, mounts and DNS files | Image lookup and container management |
| `cgroup`, `network`, `ipc` | Their Linux resource/protocol operations | CLI and durable lifecycle policy |

Within `cli`, `parse_*.go` handles command families, `commands_*.go` executes
operations with a caller-provided context, `management.go` handles cancellation
and exit status, and `output.go` handles table/JSON rendering. `help.go` is the
option reference. Parsing completes before privilege escalation.

Within `runtime`, `check_linux.go` probes capabilities, `protocol_linux.go`
defines supervisor/init messages, and `filesystem_linux.go` prepares private
roots. `runtime_linux.go` coordinates startup and supervision; `init_linux.go`
runs in the namespaces. Existing terminal, exec, security, and recovery files
remain responsible for their respective operations.

## Execution and ownership

Foreground: parse → privilege/delegation setup → acquire template → capability
checks → prepare private root → allocate resources → start init → authorize
workload → supervise → clean up.

Detached: create a durable record → launch a systemd supervisor → follow the
same runtime path → record completion. Observer events connect runtime progress
to durable state without making runtime depend on container storage. Managed
exec receives pinned namespace, root, executable, and cgroup resources through
the existing executor contract.

| Resource | Owner and lifetime |
| --- | --- |
| Template/image lease | `template.Template`; builtin copy leases release after copying. Image-backed overlays retain the lower image lease in both supervisor and init through namespace use; workload exec closes inherited lease descriptors. Other directory templates must stay stable while copying. |
| Transient root and run directory | Runtime; removed after workload cleanup, or preserved when safe cleanup cannot be established. |
| Retained root | Container store; reused on start/restart and removed by `rm`. Runtime stages, syncs, and atomically publishes a copied tree or a versioned overlay storage envelope. |
| Cgroup and network allocation | Runtime coordinates adapter cleanup and preserves recovery receipts when needed. |
| Exec descriptors and sessions | Runtime/executor; closed before disk cleanup. Workloads join their resource group before execution. |
| Records, log files, operation locks | Container store/supervisor; lifecycle mutations remain serialized. |

Restart validates an existing complete copied root without reopening its original
source. Overlay roots instead validate a private envelope and matching immutable
image ID, then reacquire the lower image. Both preserve container writes, and
directory copies still permit restart after their source disappears. Temporary
mounts and execution resources are fresh for each generation. Image acquisition
must protect the lower throughout overlay use or until a copy/durable reference
exists; do not reverse image/container lock ordering.

The boot-enabled `casklet-restarts.service` owns automatic policy reconciliation.
It uses the same per-container operation lock and start path as manual lifecycle
commands, with four bounded concurrent operations and fair polling. Workload
units keep `Restart=no`; every attempt publishes a new durable generation and
receipt. Retry count, backoff deadline, and manual-stop state survive manager
replacement. Boot identities distinguish policy restoration from a manual stop
in the current boot. Manual stop records suppression before signalling, and
manual start/restart clears it only when an execution is published. Preparation
failures consume retries without inventing an execution. Guest automatic starts
have no Mac port preflight; forwarding remains Lima's responsibility. Engine
installation enables the manager and replaces it independently of workloads.

Log rotation and snapshots use a per-container `.logs` lock. Lookup and lock
acquisition briefly use the metadata lock; waiting and log I/O do not. Removal
claims the log lock before hiding a record. A bounded lock timeout discards only
the current capture chunk, records truncation and a diagnostic, then continues
capturing later output. Permanent storage errors still drain the pipe so output
failure cannot indefinitely block the workload.
Initial log tails validate and pin every retained segment before searching backward
with one 32 KiB buffer. Only the selected suffix is allocated and read; a zero tail
pins the current end without reading history. The same locked snapshot establishes
the follow cursor, preserving rotation and generation fencing.
Active supervisors from an older engine keep using the metadata lock; readers
honor that protocol until completion or a new supervisor enables log locking.

Managed runtime completion returns an observed exit reason and OOM flag
independently of the command exit code and cleanup errors. The supervisor saves
them in the terminal record and generation receipt. OOM is sampled before cgroup
removal; timeout and forwarded-stop reasons are recorded at their owning runtime
branches rather than inferred from numeric exit codes. Supervisor-loss recovery
records an explicit unknown-command-status reason. Restart clears current results
and retains the previous generation's reason. Empty legacy fields stay compatible.

Runtime cleanup returns typed failures without changing the command's exit
code. The supervisor stores sanitized `cleanup_failures` stages in both the
record and its execution receipt, and detailed diagnostics in the private error
field and logs. Public inspection exposes stages without private error text.
Restart clears current failures and retains the previous execution's stages.
Failed cgroup emptying or removal preserves the run directory and its recovery
receipt. Foreground CLI invocations report infrastructure errors as status 125
when the command would otherwise return zero.

Opening, creating, listing, and removing container records opportunistically
recover abandoned `.create-ID` and `.remove-ID` directories. Recovery checks
private ownership, existing leases, and mount points. A stable `.transactions`
lock outside those directories fences deletion after internal lock files have
been unlinked. Live deletion causes opportunistic recovery to skip; recursive
deletion runs after releasing the metadata lock. Unsafe artifacts are preserved
with an error, and unknown directory names are left alone.

## Extending the project

- New CLI options: parse in the command family, validate shared semantics in `config`, then pass values to the owning module. Keep help and usage examples consistent.
- New lifecycle operations: add container-store behavior and generation/locking rules first, then expose a CLI handler. Preserve recovery and previous-execution receipts.
- New isolation features: add a Linux adapter or runtime step with explicit resource ownership, rollback, and unsupported-platform behavior.
- New storage sources: extend template resolution while preserving leases and canonical source/bind validation; keep copying in `rootfs`.

Use unit tests for invalid inputs, cancellation, and failure cleanup; validate
runtime changes with privileged integration tests in the dedicated Linux VM.
No new framework or global service registry is needed to add a command or adapter.

## OCI images

The macOS client owns login/logout and native Security/CoreFoundation Keychain
access through purego, retaining CGO-disabled release builds. A separate non-PTY
SSH preparation session resolves cached IDs before requesting credentials. On a
miss the host responds through stdin with one bounded registry-scoped envelope;
the guest attaches it only to that pull context and returns an immutable ID for
normal execution. Workload stdin, TTY handling, retained configuration, and guest
disk state never carry credentials. Authentication is lazy and missing Keychain
entries preserve anonymous access. `image` uses the registry library's challenge
handling with an additional redirect guard; registry-declared token endpoints are
explicit credential targets while other redirects lose authorization headers.
Authenticated pull errors are sanitized before presentation.

The guest image adapter uses go-containerregistry for OCI/Docker registry transport,
anonymous and authenticated Basic/Bearer challenges, manifest lists, and compression. It does not invoke
Docker Engine. Downloads select the native Linux platform and verify each layer's
uncompressed digest before applying it to a private staging root. Whiteouts precede
same-layer additions. Root-confined operations interpret absolute symlinks relative
to the image, never the host. Unsupported special files fail the transaction.

Image records contain execution defaults and the source manifest digest; the local
content identity also covers numeric ownership and defaults. Atomic reference files
map normalized registry names to immutable local IDs. Pull refreshes references;
run reuses a cached reference. Private leased transactions prepare pulls/imports
outside the global image lock. Stable per-reference locks serialize refreshes
before remote resolution; global locking protects staging creation, recovery
claims, short publication/reference updates, and deletion claims. A shared
per-image lease pins content while full verification runs outside the global
lock. Stable external transaction locks fence recursive staging cleanup after
internal lease unlinking.
Deletion validates the image lease, container references, and mount state before
atomically renaming the image to a random `.delete-` tombstone under the global
lock. Its exclusive `.transaction-.delete-` lock is created before the rename and
held through cleanup and directory syncing. Recursive removal runs outside the
global lock, checks cancellation, and uses pinned directories with no-follow,
no-cross-mount child opens. Recovery claims at most 32 abandoned transactions per
batch under the global lock, then cleans them outside it. Legacy `.import-` and
`.remove-` trees migrate to the new deletion protocol; older engines ignore the
new tombstones. Failures retain recoverable storage, and active external owners
remain protected even when their internal `.lease` file is already gone.
The `.blobs` subdirectory caches compressed layers by digest. Stable per-digest
locks serialize download/publication and hold shared usage leases through ordered
extraction. Three workers per pull fetch distinct blobs; errors cancel and join
workers before image staging closes. Manifest digest/size checks precede atomic
blob publication, and cache hits hash the pinned file again. DiffIDs and whiteouts
remain checked/applied in manifest order. Prune skips active blob leases and clears
idle blobs independently of unpacked image references. The cache is not part of
the local image identity, and unpacked roots remain independent across images.
An independent stable `.cache-lock` serializes cache policy, inventories, pruning,
and eviction; these operations never hold the image metadata lock or block on
digest leases. `.cache-policy` is atomically synced and defaults to 2 GiB of
retained compressed payload; zero disables automatic eviction. Verified hits
update use time on the pinned descriptor. Policy changes and resolved pull
completion evict oldest idle blobs, rechecking their current lease and use time.
Pull maintenance is bounded to five seconds after leases close, including errors.
Active blobs may exceed this retention limit; staging and metadata are excluded.
Independent cache pruning preserves image roots and references, reports allocated
bytes and blob/staging counts, and keeps all stable lock files. Storage scans
partition `images` and `image-cache` using pinned no-cross-mount opens and shared
inode accounting, so `.blobs` allocation is reported once.
Network preparation precedes workload startup deadlines.
Operation-scoped image observers emit layer phases and throttled byte counts from
compressed streams and archive reads. The CLI owns rendering on stderr, preserving
stdout results. The Mac resolves automatic progress mode before SSH dispatch; no
PTY or output-stream merging is needed for detached runs or explicit image pulls.

OCI archive loading stages a bounded standard layout as private data in a leased
image transaction. It validates outer tar framing/types/names and all SHA-256
blobs, traverses bounded indexes to one native image, checks selected descriptor
sizes/config, and applies verified DiffIDs and whiteouts with the existing
root-confined extractor. Publication shares registry pull's normalization,
identity, fsync and atomic deduplication without creating registry references or
compressed cache blobs. Preparation never holds the metadata lock across input
or extraction. Existing image records remain authoritative on deduplication.

Archive saving acquires and verifies the shared immutable-image usage lease,
then stages one bounded uncompressed layer outside the metadata lock and emits
a standard layout directly through the Mac non-PTY binary stdout transport.
Synthesized OCI config carries supported launch/health defaults and numeric
ownership; the local identity survives a reload while manifest identity and
layer boundaries change. Directory imports are rejected to preserve their
separate identity/capability policy. Both stream commands close blocked guest
standard descriptors on cancellation, using management signal handling and
existing remote signal sessions. Temporary export preparation uses the same
recoverable transaction lifecycle as imports.

Template resolution holds an image lease while merging defaults and publishing a
durable reference or acquiring the runtime source. Container records store the local
ID and fully merged configuration. New privileged image roots use a private
versioned `rootfs.overlay` envelope beside the legacy `rootfs` path, containing
the lower ID and upper/work directories. Metadata therefore cannot collide with
files inside existing copied container roots. Init makes
mount propagation private, mounts the merged OverlayFS view at the transient run
root using pinned directory descriptors, validates it, then prepares workdir/DNS
before pivoting. The merged mount never appears in the guest host namespace.
Overlay metadata copy and directory redirects are disabled. Legacy copied roots,
directory sources, and user-namespace modes retain their copy path. Init inherits
the lower image usage lease so supervisor loss cannot authorize deletion while its
namespace survives. Runtime preserves image ownership, creates a missing working
directory, mounts private `/tmp` and `/dev/shm` tmpfs filesystems, and applies
the bounded OCI root capability policy needed by application entrypoints. Builtin
BusyBox health checks remain strict; generic filesystem validation requires neither
BusyBox nor static linking. Restart uses the retained root and saved configuration.
The saved execution config carries optional `tmp_size` and `shm_size` byte capacities;
zero values preserve legacy defaults of 16 MiB and 64 MiB. Run/config/project
options accept positive values up to 128 GiB, and public inspection applies the
same defaults. Init validates sizes before namespace filesystem mutation and mounts
them with `nosuid,nodev,noexec,mode=1777`; exec and probes join those mounts.
Linux rounds capacities up to page boundaries. These are allocation ceilings rather
than reservations, and allocated tmpfs pages use the existing workload memory
cgroup. Each execution recreates empty mounts, including retained OverlayFS and
user-namespace/rootless copy paths.

## Named data volumes

`volume` owns private metadata, data directories, creation/removal transactions,
and shared usage leases. Mount configuration stores volume names rather than host
paths. `rootfs` pins the corresponding guest data directory for bind installation.
Runtime holds a usage lease through cleanup and passes it to init with close-on-exec,
so abrupt supervisor death does not release an active namespace's lease. Detached
creation holds leases until records provide durable references. Removal acquires
an exclusive lease and checks all container configurations. Lock order is volume
store then container store; lifecycle operations never acquire volume locks while
holding container metadata locks. User namespaces are currently excluded.

Volume tar export holds an exclusive usage lease without requiring retained
references to disappear. Restore extracts to leased private staging outside the
global volume lock, validates a complete bounded archive, fsyncs it, and publishes
a new name under that lock. Transaction recovery skips leased staging. The CLI
uses stdout/stdin streams, so macOS archives need no filesystem share.

## Guest disk accounting

`storage` measures allocated blocks using pinned no-follow directories and
`openat2` mount-boundary checks. It counts hardlinks once per category and skips
vanished entries during live writes. `image.Prune` snapshots IDs, then checks each
candidate under the existing deletion lock/lease/reference protocol. The CLI owns
preview selection and presentation; disk reporting never deletes workloads or
volumes. Filesystem availability covers the guest OS disk, while category totals
cover only casklet storage and exclude host shares.

## Container health

Image launch defaults retain the Docker Healthcheck extension and custom shell;
raw config decoding preserves StartInterval independently of registry-library
coverage. Config owns validated probe tests, timing defaults, and field overrides.
Each detached supervisor starts one monitor after publishing command startup.
The monitor uses pinned exec resources, a dedicated child cgroup, and immediate
deadline/cancellation kills. It finishes before exec descriptors or workload
resources close. Generation-fenced state updates retain five bounded results,
without probe output. Container inspection separates health from lifecycle state;
restart resets health while retaining configuration. Foreground execution has no
durable health monitor. Health transitions do not change restart policy.
Readiness waits resolve an immutable ID and generation once, then sample the saved
health state on a cancelable timer while recovering stale supervisor state through
the existing lifecycle path. They hold no lifecycle lock between samples, schedule
no probes, and fail on stop, removal, or generation changes. The CLI owns the
30-second default deadline and maps readiness timeout to status 124 without
changing the workload. Ordinary exit waits continue reading generation receipts.


## Named networks and scoped DNS

`network` owns guest-local named metadata, stable store/deletion lease files,
per-execution allocation journals, bridges, and scoped DNS transport. `container`
checks retained references and reserves exact names/aliases atomically under its
metadata lock. Creation acquires the network lease before publishing the retained
config; runtime holds a lease through execution cleanup. Removal acquires the
network store lock and exclusive lease before scanning containers, then takes
the existing network allocation lock. Runtime cleanup reads immutable metadata
under its execution lease without reversing that lock order.

Named allocations use `network-named.json`; legacy `network.json` remains byte
schema compatible so pinned old supervisors can continue scanning it. A separate
forwarding receipt saves the original setting before marking the legacy shared
receipt to preserve forwarding while old supervisors cannot see named journals.
The new engine restores forwarding only after both allocation kinds are gone.
Current-boot ownership groups protect link mutation; failed cleanup retains the
named journal until the final bridge has been removed. Metadata publication is
atomic and fsynced; interrupted save files are reclaimed under the stable lock.

Each supervisor owns a small UDP/TCP DNS server bound to its network gateway and
an ephemeral port. Per-execution NAT redirects only that container's gateway DNS
requests. Queries inspect current-boot journals and existing runtime lock leases,
so abandoned/stopped executions are excluded and replacement addresses have no
server cache. Exact names and aliases (optionally `.casklet`) receive IPv4 A records;
external names use execution-specific upstream servers. There are at most 32
concurrent requests, 4 KiB packets, one TCP question per connection, and bounded
upstream/connect/read deadlines. Shutdown cancels accepted and upstream sockets,
quiesces workers, and keeps listening socket reservations through NAT cleanup.
No persistent DNS daemon or new engine re-exec mode is required. Named bridge
firewall drops restrict forwarding to other casklet networks without modifying
unrelated host tables or policies. Published host ports retain the existing Lima
address translation and forwarding path.

## Project orchestration

The CLI expands a project file on the invoking host before VM operations. `project`
validates the bounded dependency DAG and owns operation sequencing, resource
receipts and rollback. It calls the existing executable's command paths, preserving
macOS Keychain preparation, path/share translation, host-port preflight, readiness,
generation fencing and managed lifecycle policy. No long-lived project daemon is
introduced. Config hashes include effective defaults and expanded environment,
with network aliases sorted only for hashing. Legacy hash reuse inspects the
pinned container ID and verifies the desired configuration against its saved alias
order before any deployment mutations. Ownership receipts store only network/volume
names and creation times. Private service comparison receipts additionally store
per-field digests bound to container IDs and their configuration labels; no
configuration values are retained. Immutable container labels carry the project/service/config
identity and one startup token. Explicit external-volume mappings resolve logical
mount sources to existing guest names before config hashing. Their existence is
preflighted with other resources, and they never enter creation/rollback ownership
lists. After successful preflight, any old receipt for an external name is released
and synced before guest mutations; config-free down therefore preserves it even
after same-name restore or managed-to-external conversion. Startup failure does
not reverse that explicit release. Other projects retain their own receipts.

`project up --recreate KEY` explicitly replaces selected service records even
when the desired hash matches. It preflights all replacement/new sources and
saved port/alias reservations before removal, resolves cached image references
with existing credential preparation, and pins the selected immutable image.
Matching stopped services preflight their actual retained sources and saved Mac
host ports. Only identical selected running HostIP/HostPort/protocol endpoints
are exempt from the first host check; all endpoints are checked again after
removal with bounded forwarding-socket release waiting.
Each private guest removal uses the existing operation lock across generation/
project-label verification, stop and remove. Selected old records are removed in
reverse dependency order before topological creation. Unselected running IDs and
generations, managed/external volumes and bind data are preserved; selected
private roots/logs are discarded. Removed old records are not automatically
restored after partial failure. Existing transaction-token rollback removes only
new records/resources; retry creates absent services.

`project up --dry-run` shares inventory/ownership checks and reports resource
creation plus service create/reuse/start/recreate actions, reasons, and sorted
changed field names. Unselected drift is reportable during preview but still
requires explicit recreation when applied. Only ID/spec-matched private baselines
supply field differences; legacy/stale baselines are explicitly unavailable.
Parsed-intent digests avoid comparing explicit options against image-inherited
defaults. Successful up publishes comparison history after final identity checks;
preview does not write it, and down discards comparisons for removed services. It never
pulls images, validates source readiness, runs health waits, changes container or
resource deployment state, or saves/releases ownership receipts. Mac inventory
calls require an already-running VM under the machine lock; stopped/missing VMs
fail with an explicit start hint. Engine installation/repair on a running VM and
local project locks/metadata initialization remain permitted. Preview is a
snapshot; lifecycle generation fences and live port checks still run at apply.

`project up --wait` applies one deadline after parsing to local lock acquisition,
VM preparation, startup and readiness. The runner pins each service ID/generation
after startup and samples existing health state through sanitized inspection,
including image-inherited checks. Services without enabled checks require running
state. Each pass revisits all services; stopped or changed executions fail. Final
records recheck the pinned executions and readiness. Timeout returns 124 after the
existing independent bounded rollback; reused/resumed services remain.

A stable cancelable lock serializes each project in the local machine state directory.
Atomic synced receipt writes record successfully created resources. Private bounded
host/guest resource commands use exclusive creation and timestamp-conditional
removal, with the identity comparison under the existing network/volume metadata
lock before lease/reference checks. This closes the inspect/remove name-reuse race.
Rollback uses the current startup token and exact IDs, independent of caller
cancellation, with a two-minute bound. Existing resumed services are preserved.
Unverified creations after abrupt transport/process loss remain for inspection.

## Resource monitoring

The CLI resolves and deduplicates a statistics selection to immutable IDs once.
Container multi-sampling pins each execution cgroup, reads starting counters,
waits once for the entire set, reads final counters, and rechecks ID/generation.
Removed or changed executions expose unavailable values. Each watch frame obtains
fresh readers, so restarts never share counters. Readers close before output and
between frames. The CLI throttles unavailable frames, owns cancellation/output,
and streams table frames or newline-delimited JSON through the existing guest session.
