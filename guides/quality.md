# Completed quality work

[Development](development.md) · [Architecture](../ARCHITECTURE.md)

Use this record when reviewing the project. Check the current implementation
and regression tests before proposing an item already listed here. Reopen an
item only with a reproducible regression or a distinct uncovered scenario.

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
