# Manual session creation recovery

[中文](MANUAL_SESSION_CREATION_RECOVERY.zh-CN.md)

## Ownership and recovery

Desktop registers new requests, startup recovery and explicit retries with one
process-local creation manager. A request retains its operation, session and
topic identities across interruptions. The existing OS creation lock is the
cross-process authority; a lock file's existence or age never grants ownership.

If another process owns the lock, the manager keeps the request pending and
retries with jittered intervals of 0.5, 1, 2, 4 and at most 5 seconds. Startup
recovery is armed after tab restoration and rescans every 30 seconds. Display
polling does not initiate or own recovery. After acquiring the lock the worker
reads the current revision and phase again. Failed operations require explicit
retry, bound to the observed revision; a stale retry cannot restart a newer
failure.

After runtime construction, a transient result-save error retries persistence
with the same result and lock. It does not repeat runtime construction. Invalid
identities, incompatible states and unsupported database versions remain visible
as blocked operations. Archived or deleted sessions are not revived.

Recovery never changes the selected session. A newer navigation intent wins over
an earlier creation's completion. A topic activation claims its terminal event
before publishing readiness, so switching again while background pruning is
pending cannot emit a second cancellation for an already-ready request.

## Progress and diagnostics

The existing Begin/Get/List/Retry RPCs retain their arguments and add an optional
`progress` response. Its status is `queued`, `running`, `waiting_lock`,
`retrying_storage`, `blocked` or `stopping`. Stage, start time, elapsed milliseconds,
next retry time, slow flag and a sanitized error code are observational only.
Clients must tolerate missing or unknown progress values.

The recovery notice offers **Export creation diagnostics**, implemented by
`ExportManualCreationDiagnostics()`. The JSON contains build identity, current
local task snapshots and the latest 256 stage/attempt events. It can be exported
without reading the session database and contains no chat, attachment, provider
configuration or raw service-log content. Other processes' stages/PIDs are not
inferred. The service log also records stage transitions and rate-limited slow
stage warnings. A 30-second slow threshold is diagnostic, not a takeover timeout.

If initialization remains stuck, export this report from the affected running
application before restarting. Record the triggering action and approximate
time. The report narrows the blocked stage; it does not establish the cause of
every historical `starting` record.

## Shutdown and compatibility

Shutdown freezes admission, stops scanning/retries, cancels builds and joins
their actual executions before closing shared resources. Superseding a build's
notification cannot complete that join. If joining exceeds ten seconds, shutdown
reports a retryable failure and retains resources/ownership; it does not close
the database or report clean exit. Interrupted creation remains recoverable on
the next start. There is no new user cancellation action.

SQLite schema, lock paths, identity derivation and persisted phases
(`reserved`, `starting`, `ready`, `failed`) are unchanged. Phase/error updates
preserve unknown JSON fields. Local progress is not written to creation records.
Older readers ignore the optional RPC field; newer readers accept older records.

## Verification

Run focused tests from the independent Desktop Go module:

```sh
go test -race . -run 'TestManualCreation|TestComposerRestart|TestShutdownServiceFailure|TestArchiveLastVisibleSession' -count=1
node ../scripts/desktop-windows-go-tests.mjs --all
```

The tests use disposable state and real subprocess locks. They cover owner exit,
two competing managers, revision changes while waiting, result-only retries,
superseded build completion, shutdown ownership, prepared-storage reuse, unknown
fields and archive-then-create. Run these on Windows x64 and macOS; cross-compiling
does not qualify native locking or shutdown behavior.

Frontend checks include `manual-creation-recovery.test.tsx`,
`formal-submission-recovery.test.tsx`, composer lifecycle and navigation tests.
`node bench/manual-creation-recovery.mjs` exercises the notice in Chromium with
the actual component and a mock host. Set `CHROME_EXECUTABLE` if using an installed
Chrome instead of Playwright's browser. The browser test does not qualify native
file-dialog behavior.
