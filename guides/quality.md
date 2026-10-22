# Completed quality work

[Development](development.md) · [Architecture](../ARCHITECTURE.md)

Use this record when reviewing the project. Check the current implementation
and regression tests before proposing an item already listed here. Reopen an
item only with a reproducible regression or a distinct uncovered scenario.

## 2026-10-22: Require pinned mixed-generation upgrade validation in CI

Both native Linux privileged jobs build the exact pre-named-network revision
`e470555a6df39f95823c1ab50dc9978f335b38f5` in a private source directory using
the current patched Go toolchain. The shared-VM entry point validates the baseline's
native ELF architecture and preserves the required flag across sudo. CI requires
the existing pinned-supervisor network/forwarding regression to execute; missing
baseline configuration and occupied bridge journals fail rather than skip. Local
runs retain their optional baseline behavior unless explicitly required.

Validation: macOS formatting, complete unit tests and vet, and pinned actionlint
passed. Both privileged CI architectures must pass the actual mixed-generation
case before this change is merged.

## 2026-10-21: Sample readiness from pinned service IDs

Whole-project readiness passes send the startup-pinned full container IDs to the
existing minimal batch status command. The guest validates at most 64 unique IDs
before lookup, reads and refreshes only those records, and releases metadata
locks before lifecycle recovery. Missing or concurrently removed IDs remain
missing in the response and fail the existing host fence. Project/service labels,
execution generations, effective health, shared deadlines, dependency resampling
and rollback semantics remain unchanged. The private name-only diagnostic sampler
remains supported. Store opening still enumerates transaction directory names;
this change removes unrelated state decoding and lifecycle queries from each pass.

With one running project service and 256 stopped unrelated records, real Linux
CLI sampling opens state files 257 times with the name-only sampler and once
with pinned IDs, returning the same ID/generation/state. The bounded inotify
regression extends the existing oversized-history fixture and retains previous
inventory/reservation read-count assertions. Unit regressions cover exact 16/64
service requests, full-ID bounds, duplicate/name/prefix rejection before reads,
unrelated corrupt state, effective health, refresh lock release, missing/removal,
cancellation, refresh failures and the existing host generation/ownership fences.
The opt-in macOS sampling benchmark now exercises the pinned request.

Validation: native macOS arm64 client/embedded-engine build, formatting, complete
unit/vet and CLI/project race checks passed. Linux arm64 complete unit/vet and
container/CLI/project race checks passed. All eight privileged project integration
cases passed through the shared-store preservation wrapper, including readiness,
rollback, preview/recreation, offline images and retained-history read counts.
All 14 scoped real macOS project tests passed through SSH/VM. Intel execution,
the opt-in timing benchmark and the complete privileged/macOS end-to-end suites
were not run for this change.

## 2026-10-21: Page project records at the storage boundary

Project inventory and reservation calls use storage-level ID cursors instead of
loading and refreshing the entire store before slicing each response. The store
skips preceding and excluded IDs before reading state, applies immutable label
selection before lifecycle refresh, and retains at most 64 selected records.
Supervisor recovery and effective-health sampling run after the metadata lock is
released. Concurrent removal refills short pages; filtered traversals can end
with an empty final page. Host cursor validation, private-field redaction,
creation-order rollback and generation-fenced reservation snapshots are retained.
Directory names are still enumerated per page; no persistent index is introduced.

Linux inotify regressions count actual state-file opens. With 256 stopped history
records across four real CLI pages, the previous engine opens state 1,024 times
for inventory and 1,280 times for reservations; the updated engine needs 256 and
512 respectively. Reservation snapshots intentionally reread their selected state
with configuration. Both read-count assertions fail against the previous engine.
Other regressions cover sparse immutable selection, skipped corrupt prior/excluded
state, selected-state validation, refresh lock ordering, concurrent-removal refill,
empty final pages, cancellation and refresh failures.

Validation: native macOS client/embedded-engine build, full macOS unit/vet and
formatting checks, and CLI/project race checks passed. Linux arm64 full unit/vet
and container/CLI/project race checks passed. All eight privileged project cases,
including oversized history, offline images, preview/recreation, readiness and
rollback, passed through the shared-store preservation wrapper. All 14 scoped
real macOS project tests passed through SSH/VM, including deployment, preview,
recreation, logs, port/alias rejection, cancellation and rollback. Intel execution
and the complete privileged/macOS end-to-end suites were not run for this change.

## 2026-10-21: Batch project reservation preflight

Preview and selected recreation replace per-container full inspections with
private ID-ordered pages of at most 64 configuration snapshots. Only network,
aliases, published ports and lifecycle identity cross the transport. Selected
old IDs are excluded before configuration reads. Existing metadata snapshots
bound each read, without holding a global lock across a page. The host retains
initial name/generation fences, detects missing records and malformed pages,
keeps conservative initial port claims and honors cancellation between pages.
Mac ports use inspection's existing guest-to-host address mapping. Legacy alias,
source readiness, selected-port and lifecycle removal checks remain unchanged.

The 128-container preview regression reproduced 128 inspect calls before the
change and now requires two reservation calls with no per-container inspect.
Additional regressions cover bounded snapshots, excluded selections, private
payloads, missing/changed/duplicate identities, invalid ports/states/cursors,
write errors, cancellation and Mac forwarding-address conversion. Empty/excluded
inventories require no reservation call. Full macOS formatting/unit/vet/race
checks and native client/embedded-engine build passed. Ten real Mac project tests
passed, including a new unrelated localhost-port/alias conflict test that keeps
the selected ID/generation running through preview and apply rejection. The new
test passed again against the final build. Linux arm64 unit/vet and CLI/project
race checks plus eight privileged project cases passed through the shared-store
preservation wrapper. Intel Mac execution was not run locally.

## 2026-10-21: Page project inventory without unrelated command payloads

Project operations page their private guest inventory by full container ID, at
most 64 records per response. Initial inventory preserves complete project member
records and minimal unrelated name/state/generation/creation identities for ownership and
reservation checks. Final records, rollback, down, ps and logs use project-scoped
pages. Each response keeps the 8 MiB operation limit, while total retained
history no longer has to fit one response. Public member JSON remains complete.

The host validates page size, increasing full IDs, project scope and advancing
continuations, and checks cancellation between pages. Existing per-container
generation and ownership validation still handles live inventory changes.
Validation includes oversized unrelated command history, private-field exclusion,
full member preservation, unrelated name ownership, malformed pages/cursors,
empty results, creation-order rollback, cancellation and writer errors. A real
Linux regression creates 256 retained records exceeding 8 MiB: the previous
engine fails before startup, while the updated engine completes up/wait, ps,
logs, preview and down while preserving all unrelated records.

Validation: full macOS formatting/unit/vet/race checks and native build passed.
Nine real Mac project tests passed, including preview/recreation, readiness,
logs and cancellation; final rollback tests passed again after preserving
creation order. Linux arm64 unit/vet and CLI/project race checks passed, as did
eight privileged project cases through the shared-VM preservation wrapper.
Intel Mac execution was not run locally.

## 2026-10-21: Cancel project logs through blocked output pipes

Project log aggregation reuses the archive stream's pollable descriptor helper
for stdout and stderr, closing private duplicates on cancellation and restoring
caller descriptor flags after all readers join. Output failures cancel readers
immediately, before waiting for subprocess completion; stream failures cancel
before attempting a final partial-line flush. Workloads remain running.

Validation: inherited full stdout/stderr pipe regressions failed before the fix
and pass afterward. Writer-failure tests verify cancellation before stream return.
The real macOS client/SSH/VM tests pass normal selection/following, SIGTERM status
143 and cancellation with a full output pipe while preserving the workload.
Formatting, full macOS unit/vet/race checks and native client/embedded-engine
build passed. Linux arm64 unit/vet and CLI/project race checks passed, as did
the selected privileged project and image/volume archive cases through the
shared-VM preservation wrapper. Caller descriptor preservation remains covered
by the shared stream regressions. Intel Mac execution was not run locally.

The PR vulnerability gate found GO-2026-6809 in the existing Go 1.27.2 Linux
engine. The minimum Go version, fresh-development archive checksums and bilingual
build prerequisites now use Go 1.27.3. Checksums come from the official Go
download manifest; final validation uses that patched toolchain.

## 2026-10-17: Batch project readiness sampling

The readiness wait loop uses one project-scoped minimal guest response per pass,
then verifies every pinned full ID, service/project label, execution generation
and required health state. At most 64 minimal status entries exclude commands,
configuration and probe history, bounding the private transport response.
Initial healthcheck discovery and final record validation remain unchanged. Sampling still revisits dependencies after each
wait and uses the original deployment deadline and rollback behavior.

Measurement: three timed samples per strategy on the real macOS arm64/SSH/VM
path, excluding 16/64-service project setup/teardown. Legacy per-service inspect
versus minimal batch status averaged 4.137 s versus 0.295 s for 16 services, and
18.176 s versus 0.455 s for 64 services. This measures a ready-state sampling pass,
not complete deployment latency. The opt-in bounded benchmark remains in-tree.

Validation: 16/64-service call counts and identity/generation/ownership checks,
missing/duplicate/malformed responses, stopped/failed health, dependency resampling,
shared deadlines, cancellation and existing rollback regressions. Complete macOS
formatting/unit/vet/race checks and native build passed, as did five real macOS
tests for project deployment/wait/rollback, logs and doctor. The final bounded
benchmark passed with three samples per strategy at both sizes. Linux arm64
unit/vet and CLI/project race checks passed. Bilingual examples and maintained
local documentation links remain aligned; all 17 script tests passed. All 162
privileged Linux tests passed through the shared-VM preservation wrapper; only
the optional pinned legacy-supervisor test was not configured.

## 2026-10-17: Report structured doctor diagnostics

Doctor retains automatic VM preparation and now emits text or schema-versioned
JSON with client/engine identities, bounded Lima metadata, configured resources,
guest data filesystem bytes and named runtime/filesystem/preparation checks.
Failed checks return 125 with a report; low space remains advisory. Progress stays
on stderr and local shares/SSH paths are excluded from VM metadata. The Linux
engine reports guest information only. Filesystem sampling does not walk state.

Validation: report success/failure/low-space and writer failures, no-Lima JSON,
option/path ordering, read-only machine metadata and scoped help regressions.
Complete formatting/unit/vet/race checks, native build and real macOS JSON/text,
missing-Lima, invalid-template and help tests passed. Linux arm64 unit/vet and
CLI/machine race checks passed; the full privileged suite passed 162 tests via
the shared-VM preservation wrapper, with only the optional pinned legacy case
unconfigured. Final doctor/launch regressions passed after the metadata change.
The destructive macOS provisioning fault test remained gated to a disposable VM.

## 2026-10-17: Aggregate project service logs

Added project logs with repeatable logical service selection, per-service tails
and following through existing generation-fenced container readers. Selection
pins full IDs once, detects missing/duplicate services and bounds readers to 64.
Read-only execution avoids the deployment store lock. Bounded per-stream line
buffers and serialized prefixed writes prevent mixed lines; failures cancel and
join peers. Help, usage and bilingual overviews describe snapshot membership,
ordering, partial lines and long-line splitting.

Validation: selection/parser, fragmented/concurrent output, bounded long lines,
writer-failure cancellation and reader joining regressions; scoped help parity.
Complete formatting/unit/vet/race checks and native build passed. Real macOS
regressions verify tails, service filtering, following, SIGTERM exit 143,
concurrent project up and surviving workloads; native scoped help parity passed.

## 2026-10-16: Explain deployment previews with redacted field changes

Project preview now reports deterministic reasons, sorted changed field names,
explicit recreation requirements and unavailable comparison history. Unselected
drift is reportable during preview; applying it still requires explicit selection.
Ownership, removed/duplicate services and reservation checks remain enforced.

Successful up saves bounded private per-field digests, pinned to container IDs and
configuration labels, after final identity checks. Comparison uses canonical
parsed intent instead of image-inherited defaults. No raw configuration values
or comparison digests enter preview output; no configuration values enter saved
comparison history. Matching legacy up can backfill history; missing/stale history
is explicitly unavailable. Preview does not save state, and down discards removed
service comparisons. Resource ownership receipts retain their existing semantics.

Validation: focused tests cover field changes, explicit/legacy recreation,
provenance, failed-up history, comparison-publication failure rollback, down
cleanup, invalid persisted comparisons and redaction. Real Linux/macOS tests verify preview leaves IDs/generations and state
bytes unchanged, applying drift still needs selection, selected replacement works
and legacy field details are not guessed. Formatting, unit/vet/race and native
build checks passed. All 161 privileged Linux tests passed through the shared-VM
preservation wrapper; the optional pinned legacy-supervisor test was not configured.
Four real macOS tests cover preview/recreation, existing replacement recovery,
offline validation and help parity. The final publication-failure regression also
passed project race tests on both macOS and Linux arm64.

## 2026-10-16: Validate project configuration offline

Added `project validate --config FILE [--json]` using the existing deployment
parser, including strict fields, dependencies, resource scoping, conflicts, option
limits and config-relative env-files. Execution returns before guest-call or
project-store setup. Summaries expose project/service keys only; invalid input
keeps the existing sanitized parser diagnostics and scoped help. Both help forms,
the usage guide, architecture and bilingual quick starts describe the boundaries.

Validation: regressions reproduced the missing command before implementation.
Host-local tests run with Lima absent and machine state unavailable, verify no
state is created, validate dependency order and suppress environment/command
values. Invalid fields, dependencies, duplicate keys, missing env-files and flags
fail with focused help. Formatting, complete Go unit/vet/race checks and native
macOS command validation pass; CI also validates both guest architectures.

## 2026-10-16: Retain structured container exit reasons

Managed runtime completion now returns an observed reason and OOM flag separately
from command status and cleanup errors. Detached records, public inspection,
JSON listings and generation receipts preserve both. Reasons distinguish completed
commands, kernel OOM, configured timeout, forwarded stop, startup/runtime failure
and supervisor loss. Numeric exits 124/137 do not imply timeout/OOM. Kernel OOM
is sampled before cgroup deletion and takes precedence when observed.

Restart clears the current result and retains it in `previous_exit`; interrupted
completion restores reason/flag from the existing receipt. Legacy records with
no reason remain valid. Bounded reason validation rejects unknown/inconsistent
metadata. Existing command statuses, cleanup failures and restart policies retain
their semantics. Help, usage, architecture and both READMEs describe the fields.

Validation: macOS arm64 formatting, complete unit/vet/race checks and native
client/embedded-engine build passed. Linux arm64 unit/vet and runtime/container
race checks passed. All 160 privileged Linux integration tests passed through the
shared-VM preservation wrapper; final reason, supervisor-loss and cleanup tests
passed again after the last schema/test changes. Four real macOS tests passed,
including reason/history transport, lifecycle retention, wait/listing and exact
foreground streams. The optional pinned legacy-supervisor case was not configured;
Intel native execution is covered by hosted CI where applicable.

## 2026-10-16: Keep downloaded documentation links usable offline

Release archives now include architecture and usage/development documentation at
its repository-relative paths. Packaging validates inline local Markdown links
inside the actual archive before executable verification and checksum publication.
The existing explicit member allowlist excludes private notes and local state.
Both README translations and release documentation describe the bundled guides.

Validation: a mocked-build archive regression reproduced the three missing
linked documents before the fix. Archive-content/byte checks and missing/escaping
link regressions pass, alongside the full Python script suite, Go unit tests,
formatting and vet. Native arm64 packaging and CI package checks validate the
real archived executable; Intel native execution is delegated to hosted CI.

## 2026-10-16: Condense the bilingual project overview

Both READMEs now focus on project scope, installation, first use, container/data
lifecycle, and VM behavior. A matching capability index links to existing usage
sections instead of repeating implementation details, option limits, and advanced
workflows. Platform prerequisites, security limitations, upgrade behavior, and
data-retention semantics remain aligned. Release CI details stay in the
development guide.

Validation: all ten shell blocks match across translations and pass shell syntax
checks; local links and heading anchors resolve, and link targets match between
languages. Diff whitespace checks pass. Documentation only; runtime tests were
not repeated.

## 2026-10-16: Check supervisor-loss cleanup at the owning lifecycle stage

`TestBackgroundSupervisorDeathRecovery` now checks removal of the recorded
workload cgroup immediately after recovery. It still verifies runtime-directory
cleanup, unknown exit status, retained failure metadata and a subsequent launch.
The systemd service parent is checked after normal container removal resets the
failed unit; a failed transient unit can retain an empty parent before that step.
No production cleanup behavior or disappearance assertion was relaxed.

Validation: recent CI failures showed an empty service parent (`populated 0`).
The original test reproduced that failure once in 30 local Linux arm64 runs.
The updated regression passed 50 consecutive privileged runs through the
shared-VM preservation wrapper. macOS unit tests, formatting and vet passed;
Linux arm64 integration-source vet passed. Intel Mac execution was not run.

## 2026-10-16: Bound non-root timeout fixtures by their explicit stop grace

The timeout case in `TestNonrootProcessLifecycle` explicitly sets a 300 ms stop
allowance inside its existing three-second total assertion. Its shell ignores
TERM so the test consistently exercises forced non-root workload shutdown.
Previously the implicit five-second allowance could legitimately exceed the
fixture's three-second assertion. Exit 124 and command-start output remain
required; the other non-root lifecycle cases retain their original coverage.

Validation: the TERM-ignoring fixture without the explicit allowance reproduced
the CI failure in 5.40 seconds. With the allowance it passed 50 consecutive
privileged Linux arm64 runs through the shared-VM preservation wrapper.
macOS unit tests, formatting and vet, and Linux arm64 integration-source vet
passed. Intel Mac execution was not run.

## 2026-10-16: Synchronize the live log-rotation fixture with its follower

`TestBackgroundLogRotationFollow` acknowledges each complete output batch through
a bind-mounted test directory before producing the next. This keeps its exact
9,021-byte assertion inside the three-segment retention window even when a CI
reader starts late or is descheduled. The fixture still writes across rotations,
checks the retained suffix and capacity, and verifies a tail crossing segments.
The workload has a 20-second timeout and acknowledgements share a 12-second bound.
Production retention-overrun behavior is unchanged and retains its unit coverage.

Validation: delaying the original follower startup by two seconds reproduced
missing output (4,925 of 9,021 bytes). The updated privileged Linux arm64 test
passed 50 consecutive runs through the shared-VM preservation wrapper. macOS
unit tests, formatting and vet, and Linux arm64 integration-source vet passed.
Intel Mac execution was not run.

## 2026-10-15: Point volume backup argument errors to scoped help

Malformed `volume export`/`volume restore` commands now suggest their existing
subcommand help instead of the volume overview. Regression cases cover missing
names, unknown flags and excess names for both commands, while unknown volume
subcommands retain the group help. The usage guide records this behavior.

Validation: the focused regression reproduced the incorrect export hint before
the fix. CLI argument/help regressions, formatting, complete Go unit tests and
vet pass after the change; named help and `--help` remain equivalent.

## 2026-10-15: Ship verified macOS download archives

Hosted CI builds native arm64/amd64 archives with the matching embedded engine,
license and both READMEs. Trusted packaging controls execute the archived binary
without Lima and verify version, commit and engine bytes. Publication requires
both packages, validates their transfer checksums, uploads the two downloads and
`SHA256SUMS` to the draft, then rechecks its identity before making it public.
Failed uploads and missing/corrupt packages leave it unpublished. Documentation
covers architecture selection, checksum verification, installation, updates and
the lack of Developer ID signing/notarization.

Validation: Python regressions cover manifest bytes, invalid version filenames,
missing/corrupt archives, upload failures and replacement drafts during upload.
Formatting, Go unit/vet checks and pinned workflow lint passed locally. Native
archive/build identity checks for both architectures are required in CI.
No real release was created or published; dedicated Mac VM gates remain required.

## 2026-10-15: Report local client and bundled engine build identity

Added `version`/`--version`, optional JSON and focused help. Queries run before
platform dispatch and report version, full Go VCS commit/modified flag, compiler
and platform. Darwin clients identify their validated embedded Linux engine by
architecture and SHA-256 without reading VM state or opening Lima/Keychain.
Missing VCS metadata and missing/invalid payloads are explicit; local inspection
still succeeds. Linux reports its own engine build without a recursive payload.

Make stamps identical versions into client/engine, using an exact version tag,
explicit VERSION or the release workflow's selected tag. Hosted CI verifies
stamped native clients/engines offline, compares both aliases and independently
checks commit/modified state and payload bytes. CLI regressions cover local
execution without Lima/state, JSON metadata, malformed arguments and help parity.
Both READMEs, usage and development documentation describe these boundaries.

Validation: macOS arm64 formatting, unit/vet/race checks, stamped native client
and embedded-engine identity checks, missing-VCS fallback, and actionlint passed.
Version aliases and JSON remained usable with no Lima in PATH and invalid
machine-state settings. The independent build checker matched actual payload
bytes and Go VCS metadata; native Linux CI also checks engine version stamping.

## 2026-10-15: Gate draft publication on both real Mac architectures

Added a main-only release workflow that validates an existing draft/tag, runs
reusable hosted checks and mandatory Apple silicon/Intel VM suites against the
same immutable commit, and grants publication rights only to the final job.
Disabled configuration, failed/cancelled/skipped checks, unmerged or moved tags
and replaced/published drafts cannot reach publication. Exact tag object,
commit and draft identity are rechecked immediately before publishing.

Mac jobs require native architecture labels and use separately checked-out
trusted runner controls, so old candidate scripts cannot disable Rosetta or
skipped-test rejection. PR code never selects persistent runners. Documentation
describes dedicated hosts, main-only environment policy and the supported
draft-to-public publication path, including the manual-publisher boundary.

Validation: ten Python release/runner tests passed, covering real local Git
ancestry/tag mutations, failure/cancellation/skips, draft replacement, native
architecture/Rosetta checks and cleanup. Pinned actionlint 1.7.12 passed all
workflows. Actual repository inspection found no registered dedicated Mac
runners; the gate remains disabled and no release was published. Real Intel
and hosted dual-Mac gate execution remain dependent on operator-provided hosts.

## 2026-10-15: Recover incomplete runtime machine provisioning

VM running state no longer bypasses dependency setup. Before engine installation
or reuse, the Mac client checks the existing completion marker, required tools
and installed static BusyBox package. Incomplete setup or missing tools rerun
the same embedded script used by initial Lima provisioning. A guest lock
serializes both paths; package downloads/lock waits and the guest process have
bounded retries/deadlines. Interrupted package configuration is resumed, missing
tools are reinstalled, and only successful initialization publishes readiness.
Already delegated user managers are preserved during repair. Project preview
rejects incomplete setup with a start hint instead of installing dependencies.
Focused help, both READMEs, development and architecture document recovery.

Shell regressions cover package outages, interrupted execution/configuration,
service setup failure, readiness publication, stale-marker missing tools,
healthy reuse and user-manager preservation. Linux additionally exercises two
concurrent provisioners with the real guest flock. Host regressions cover
preview and cancellation. The disposable Mac regression injects a package
failure, retries with `machine start`, removes `readelf` behind a matching marker,
and verifies repair without changing the engine/hash marker, boot ID, delegated
user-manager invocation, active workload or private/named-volume data.

Validation:

- macOS arm64 native client/embedded engine build, formatting, complete unit,
  vet and race checks passed. Runner isolation/cleanup tests also passed.
- Four-target Darwin/Linux arm64/amd64 vulnerability scans and Linux arm64/amd64
  cross-vet passed. Hosted macOS/native Linux checks and both complete privileged
  Linux integration suites passed on the recovery implementation.
- The real macOS arm64 recovery regression passed in 20.68 seconds in a disposable
  VM. Cold initialization, rootless execution, VM stop/start and disk expansion,
  engine repair, offline image transport, project recreation/readiness and port
  conflicts also passed in that run.
- Intel Mac execution and the optional pinned legacy-supervisor case were not
  configured. Daily VM state and data were not used for fault injection.

## 2026-10-15: Combine offline image delivery with selected service upgrades

Merged OCI archive delivery with project preview/recreation, preserving both
features' focused help, usage, architecture and quality records. Two bounded
Linux/Mac combined regression sources stop the registry, export/remove/reload an
image, deploy a project by its loaded immutable ID, and preview/apply an explicit
service environment update. They verify inherited image health checks (plus
Linux entrypoint, environment, user, workdir and stop signal), no preview workload
mutation, a new selected ID, unchanged unselected ID/generation and named-volume
bytes readable by the replacement. Config-free down retains the data volume.

Validation:

- Combined macOS arm64 formatting, full unit/vet/race checks and native client
  with embedded Linux engine build passed. Linux arm64 cross-vet and all
  package/privileged integration test executable compilation also passed.
- The complete disposable macOS arm64 suite passed all 61 tests without skips
  in 305.221 seconds, including offline-image deployment, preview/recreation,
  actual TCP/UDP reuse and stopped-service host-port preflight.
- Linux arm64 user/root unit and complete privileged integration suites passed,
  including loaded-image defaults, stable unselected IDs/generations, retained
  volume bytes, recreation failure/retry and explicit mutable-tag refresh.
- Hosted macOS/Linux checks, both Linux amd64/arm64 privileged integration suites
  and vulnerability scans passed. Intel Mac execution and the optional pinned
  legacy-supervisor case were not configured.
- Temporary VM and host state were removed after validation. The ordinary daily
  VM remained stopped and its data was preserved.

## 2026-10-14: Preview and recreate selected project services

Added repeatable `project up --recreate SERVICE` and deployment `--dry-run`,
including JSON actions. Unknown/duplicate selections fail before guest calls;
unselected drift, removed/duplicate service records, ownership and external-volume
checks complete before mutations. Preview reports create/reuse/start/recreate
without image pulls, workload/resource mutations or ownership writes/releases.
Mac preview requires a running machine under its existing lock; engine/template
repair and lock/metadata initialization on that machine remain permitted.

Actual recreation preflights new/replacement sources, image defaults, binds,
saved port/alias reservations, and matching stopped services' actual retained
sources and saved Mac ports. Image resolution uses existing credentials and
cached-tag policy, then pins the immutable ID without printing merged environment.
Only identical selected running host endpoints are exempt from the first check;
changed address scopes are conservative. Full host checks after removal wait
boundedly for Lima socket release. Each selected removal checks full ID,
generation (including zero) and project labels under one existing lifecycle
operation lock spanning stop/removal. Reverse-order removal precedes topological
creation; unselected running IDs/generations and pre-existing named/external/bind
data survive. Selected private roots/logs are discarded. Failures use bounded
new-transaction rollback and never recreate already-removed old containers;
retry creates missing services. Focused help, usage, architecture and both READMEs
state these interruption, preview and recovery boundaries.

Unit regressions cover selections/drift/ownership/resources, preview receipt
preservation/actions, same-hash recreation, actual saved reservations, host-scope
exemptions, stopped retained sources/ports, cancellation, generation fences,
partial failure/retry and unchanged service/resource identity. Linux/Mac real
regression sources cover preview without deployment mutation, full receipt
fencing, named/external bytes versus private roots, matching TCP/UDP publication
recreation and connectivity, source/unselected drift/port preflight, failed
replacement and retry. Linux mutable-tag coverage separates cached recreation
from explicit pull refresh and verifies inherited image defaults; Mac coverage
includes an occupied saved host port on an unselected stopped service.

Validation:

- macOS arm64 formatting, full unit/vet/race checks and native client with
  embedded Linux engine build passed on the combined tree.
- Linux arm64 cross-vet, all package test compilation, user/root unit tests and
  the complete privileged integration suite passed. The full macOS arm64 suite
  passed all 61 tests without skips in 305.221 seconds.
- Hosted macOS/Linux checks, Linux amd64/arm64 integration and vulnerability
  scans passed. The combined validation above covers preservation, preview,
  port/source preflight, failure/retry and offline loaded-image upgrades.

## 2026-10-14: Deliver native images through standard OCI archives

Added streaming `image load`/`image save` using the existing Mac binary SSH
transport. Loads validate bounded OCI layout tar framing/paths/types, every
included SHA-256 blob, native selection, descriptor sizes, supported configs,
DiffIDs and ordered confined extraction. Private leased transactions publish
atomically without registry tag or compressed-cache writes. Save holds a shared
image usage lease, verifies content and synthesizes one uncompressed OCI layer
with supported launch/health defaults and numeric ownership. Reload preserves
local identity, while original manifest digest/layer boundaries/tags are not
exported. OCI-only save and its explicit 4 GiB flattened-layer ceiling preserve
directory imports' existing ownership/capability semantics. Preparation and
output do not hold the image metadata lock. Focused help, usage, architecture
and both READMEs describe transport, limits, disk needs and partial output.

Regressions include an independently produced third-party OCI layout, exported
layout readability by the pinned primary reader, ownership/default/StartInterval
round trips, whiteouts, existing-image deduplication/reference preservation,
malformed/duplicate/traversal/special entries, digest/size/platform/JSON failures,
missing terminators, nonzero trailing bytes, sparse logical expansion before
filesystem allocation, discarded auxiliary layout files, cancellation, rollback, cache
preservation, directory-policy rejection and save lease/deletion fencing.
Linux/macOS integration cases cover actual binary transport, disconnected
registry execution, retained restart and blocked input/output cancellation.

Image and volume streams use nonblocking duplicates registered with Go's poller,
restore caller status flags and preserve caller descriptors. Archive output and
control signals use independent SSH connections. Cancellation also bounds host
output draining when the producer has already finished and removed its control
socket, preserving a nonzero signal status and releasing guest image leases.

Validation:

- macOS arm64 formatting, complete unit/vet/race checks and native client with
  embedded Linux engine build passed on the combined tree.
- The complete combined macOS arm64 suite passed all 61 tests without skips in
  305.221 seconds. A separate real-pipe regression passed before that suite;
  both runs covered offline transport, retained restart, blocked input/output
  cancellation and successful image removal after cancellation.
- Linux arm64 user/root unit and complete privileged integration suites passed;
  hosted Linux amd64/arm64 unit/race/vet, builds, privileged integration and
  vulnerability checks also passed. Archive tests verify offline defaults,
  retained numeric user and cancellation of actual inherited pipes.
- The optional pinned legacy-supervisor case was not configured. Intel Mac
  execution was not performed. Disposable VM validation preserved the stopped
  daily VM and cleaned up its temporary machine and host state.

## 2026-10-14: Configure temporary and shared-memory capacities

Added `run --tmp-size` and `--shm-size`, with positive byte or binary k/m/g
values up to 128 GiB per mount. Run JSON and project service run objects accept
the same string fields; explicit CLI values override run JSON. Saved configs
retain sizes across start/restart and automatic restarts. Omitted/legacy zero
fields preserve 16 MiB `/tmp` and 64 MiB `/dev/shm` defaults without changing
existing project fingerprints. Explicit capacity changes participate in drift
checks. Public inspection reports configured bytes with defaults applied.

Init validates capacities before filesystem mutation and retains the existing
private, writable `nosuid,nodev,noexec,mode=1777` mounts for copied roots,
OverlayFS roots and user namespaces. Exec and health probes share those mounts.
Linux rounds capacities up to pages; sizes are ceilings rather than reservations,
and allocated pages remain charged to the workload memory cgroup. Every new
execution recreates empty temporary mounts. Focused help, usage, architecture
and both READMEs describe these boundaries.

Regressions cover bad units, explicit zero/negative values, maximum/overflow
bounds, JSON types and precedence, persisted legacy/custom values, inspection,
exec inheritance and project identity. Privileged Linux cases cover actual
copy/overlay capacities, retained start/restart, probes/exec, non-root writable
mounts and safety flags, page rounding, ENOSPC, memory accounting and OOM ceilings;
rootless cases include custom capacities. Mac cases cover CLI/run JSON/project
transport, health, retained start/restart, non-root writes and ENOSPC.

Validation:

- macOS arm64 formatting, complete unit/vet/race checks and native client with
  embedded Linux engine build passed.
- Linux arm64 cross-vet and test executable compilation passed. The combined
  tree also passed complete macOS unit/vet/race and formatting checks after
  synchronizing restored-volume support; the later main synchronization preserved
  the exact source tree.
- The complete combined disposable macOS arm64 suite passed all 58 tests in
  248.910 seconds without skips, including VM stop/start, disk growth, custom
  capacities, health/exec, retained start/restart, ENOSPC and restored-volume
  deployments using custom temporary mounts.
- Native Linux arm64 package test executables passed as the guest user, with
  rootfs/image/volume/network/container/runtime suites also passing as root.
  The complete privileged Linux suite passed through the shared-store preservation
  wrapper in the same disposable VM, including copy/overlay mounts, rootless
  capacities, memory accounting, OOM, page rounding and the combined restored-data
  regression. The optional pinned legacy supervisor case was not configured.
  The runner exited zero and deleted its isolated VM and host state; the daily VM
  remained stopped.
- Initial feature revision 0b71dfb passed hosted macOS and native Linux
  amd64/arm64 unit/vet/race/build checks, the four-target vulnerability scan and
  both complete privileged Linux suites. Intel Mac execution was not performed
  locally.

## 2026-10-14: Reuse restored and migrated volumes in projects

Project JSON accepts an explicit `external-volumes` mapping from logical mount
names to existing guest volumes. Managed and external declarations share the
16-volume bound; strict decoding rejects nulls, types, duplicates, invalid names,
logical overlaps and conflicting actual targets. Generated mounts and service
fingerprints use the actual guest name. Existing managed-resource ownership checks
remain unchanged, and missing external volumes fail before guest mutations.

External volumes never enter creation or rollback ownership lists. After all
preflight checks pass, any old receipt for an explicitly external name is removed
with a synced save before startup. This deliberate release supports same-name
restoration and managed-to-external conversion; save failure prevents guest
mutations, and startup failure does not restore ownership. Config-free down,
including `--volumes`, preserves these volumes. Other projects' receipts are not
changed. Focused help, usage, architecture and both READMEs document the semantics.

Regressions cover schema/name/total limits, actual-name fingerprints, migration,
stale restored receipts, matching owned receipts, repeated reuse, preflight/save
failure, persisted release, config-free down and failed-startup rollback. Real
Linux/macOS cases export and restore data under the original project volume name,
redeploy it explicitly, reuse it from a fresh project, verify missing-volume
preflight and readiness rollback, and verify retained bytes after `down --volumes`.

Validation:

- macOS arm64 formatting, full unit/vet/race checks and native client/embedded
  engine build passed. Privileged Linux test sources cross-compiled successfully.
- The complete disposable macOS arm64 suite passed all 56 tests in 241.885
  seconds, including actual restoration, migration, reuse, rollback and external
  data preservation. VM stop/start and disk-growth tests passed without skips.
- Native Linux arm64 package test executables passed as the guest user, with
  rootfs/image/volume/network/container/runtime suites also passing as root.
  The complete privileged Linux suite passed through the shared-store preservation
  wrapper in the same disposable VM. The optional pinned legacy supervisor case
  was not configured. The runner exited zero and deleted its isolated VM and host
  state; the daily VM remained stopped.
- Hosted CI passed macOS and native Linux amd64/arm64 formatting, unit/vet/race
  checks, builds, the four-target vulnerability scan and both complete privileged
  Linux suites on revision b5d525c. Intel Mac execution was not performed locally.
- Initial Linux CI identified a parser test fixture without an execution source;
  declaring an explicit image made it portable without changing its assertions.

## 2026-10-13: Align inherited exec and probe configuration limits

Sealed exec/probe configurations now use the same 1 MiB serialized JSON limit
as retained container configurations, through a shared config constant. Execution
validation rejects oversized fully merged values before resource allocation or
publication. Root ownership, immutable memfd seals, exact decoding and bounded
reads remain enforced. The independent command/override request retains its
64 KiB transport bound; running supervisors keep their pinned helper until restart.

Regressions cover exact-size and escaped JSON limits, sanitized oversize errors,
immutable sealed round trips with 140 KiB of inherited/override environment, and
real execution with a 70 KiB inherited value plus a valid 4 KiB override. Linux
and macOS cases verify healthy probes, exec output and restart. Focused help,
usage and both READMEs describe the merged limit and existing transport/OS bounds.

Validation:

- The sealed-config regression failed against the pre-fix Linux test executable.
- macOS arm64 formatting, complete unit/vet/race checks, native client/embedded
  engine build, four-target vulnerability scanning and diff checks passed.
  Complete unit/vet and focused config/CLI race checks passed again after updating
  this branch to the merged relative-bind fix from main.
- The complete combined disposable macOS arm64 suite passed all 55 tests in
  213.823 seconds, including both new regressions. Root-only sealed-config tests
  and the complete privileged Linux arm64 suite passed in the same disposable VM
  through the shared-store preservation wrapper. The optional pinned legacy-engine
  case was not configured. The runner exited zero and deleted its isolated VM and
  host state; daily VM state was not used. Intel Mac execution was not performed.
- An initial macOS fixture exceeded the separate 64 KiB request bound; the final
  fixture uses a valid request while retaining the large inherited configuration.
  An initial Linux wrapper used a private root-owned executable parent and blocked
  rootless cases. Making that disposable fixture directory searchable allowed
  the complete suite to pass; no product assertion was weakened.

## 2026-10-13: Resolve config-relative bind sources before validation

Run and project JSON files now resolve relative bind sources against the absolute
configuration-file directory before mount validation. Direct CLI mount parsing
keeps its absolute-source requirement. Volume sources remain logical names, and
all existing target, protected-path, duplicate-field and rootfs-overlap checks
still apply. Invalid source bytes are rejected before path normalization.

Regressions cover relative config filenames, a different current directory,
parent-relative sources with spaces, absolute sources, readonly mounts, scoped
volumes, protected sources/targets, empty/duplicate fields and rootfs overlap.
The real macOS regression reads a relative bind through both run and project up,
checks readonly enforcement, and verifies access after restart. Focused help,
usage examples and both READMEs describe the path base.

Validation:

- The config-relative regression failed before the fix.
- macOS arm64 formatting, complete unit/vet/race checks, native client/embedded
  engine build and diff checks passed.
- The complete disposable macOS arm64 suite passed all 54 tests in 212.514
  seconds, including the new run/project relative-bind regression. The runner
  exited zero and deleted its isolated VM and host state; daily VM state was
  not used. Intel Mac execution was not performed locally.

## 2026-10-12: Whole-project readiness with a shared deployment deadline

Added opt-in `project up --wait`. Every service must become healthy when its saved
configuration enables a healthcheck (including image defaults), or running when
checks are absent/disabled. Each startup pins the full container ID and execution
generation. Readiness passes revisit all services, and final records recheck those
identities and health. Stop, exit, removal and changed executions fail rather than
following a replacement. Existing per-dependency behavior remains without the flag.

One absolute deadline covers local project lock acquisition, guest preparation,
startup, dependency waits and final readiness after parsing. Deadline failures
return 124; service/infrastructure failures remain 125, and signal cancellation
retains its original status. Existing bounded independent rollback removes only
this invocation's new verified resources; reused/resumed services remain. Failure
classification is captured before cleanup so slow rollback does not turn an earlier
service failure into a timeout. Focused help, usage, architecture and bilingual
quick starts describe the opt-in behavior and cold-start timeout sizing.

Regressions cover inherited leaf healthchecks, unhealthy recovery, absent/disabled
checks, unchanged default startup, startup/local-lock deadlines, a shared absolute
budget, caller cancellation/deadlines, identity/generation/lifecycle changes, final
state changes, reused-service preservation, rollback failures and slow cleanup.
Linux and macOS suites add real leaf readiness and deadline rollback; the macOS
cancellation regression covers both dependency-only and whole-project waiting.

Validation:

- macOS arm64 formatting, full unit tests, vet, full race checks and native
  client/embedded engine build passed.
- After synchronizing the log-tail feature from main, the complete disposable
  macOS arm64 suite passed all 53 tests in 209.023 seconds. It includes real leaf
  readiness, timeout rollback, reused-service preservation, both cancellation
  modes, rotated tails, VM stop/start/resize, health, storage and terminals.
  The runner exited zero and deleted its isolated VM and host state; the daily
  machine remained running. An earlier run rejected a numeric health-retries
  value in two new test fixtures before deployment; correcting it to the existing
  string schema allowed both tests and the complete suite to pass.
- Hosted CI passed macOS and native Linux amd64/arm64 unit/vet/race/build checks,
  the four-target vulnerability scan and both complete privileged Linux suites
  on combined feature revision 7c7b6b4. Intel Mac execution was not performed
  locally. This final validation-record update retains the same PR gates.

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
- The complete disposable macOS arm64 suite passed all 51 tests in 193.728
  seconds, including rotated/zero-tail output, VM stop/start, resize, repair,
  health, projects, storage and terminals. The runner deleted its isolated VM
  and host state; the daily VM was not used.
- Linux arm64 log regressions passed as root in that disposable VM, three runs.
  Three ten-iteration samples of the four-4-MiB fixture measured approximately
  16.78 MB of heap allocation for a full snapshot (including the original main
  implementation), 5.36 kB for an initial 20-line tail and 5.04 kB for a zero tail.
  These are fixture allocations, not total process memory or general throughput.
  Tail benchmarks omit byte-throughput reporting because historical bytes are
  deliberately skipped. Intel Mac execution was not performed locally.

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
