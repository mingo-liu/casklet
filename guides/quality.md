# Completed quality work

[Development](development.md) · [Architecture](../ARCHITECTURE.md)

Use this record when reviewing the project. Check the current implementation
and regression tests before proposing an item already listed here. Reopen an
item only with a reproducible regression or a distinct uncovered scenario.

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
- `make build` and `mdocker doctor` passed with the native Darwin client and its
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
