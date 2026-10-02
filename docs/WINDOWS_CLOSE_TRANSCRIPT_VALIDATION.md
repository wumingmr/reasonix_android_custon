# Windows close and transcript diagnostics validation

[简体中文](WINDOWS_CLOSE_TRANSCRIPT_VALIDATION.zh-CN.md)

This note records the validation boundary for the Windows close-race fix and
the transcript follow diagnostics added on `main-v2`. It does not claim that an
unknown transcript synchronization root cause has been fixed.

## Implemented behavior

- The title-bar minimize, maximize, maximize-state, and close actions go
  directly through the preload IPC to Electron. Compatibility calls using the
  old generated Go command names are handled by the same native window owner.
- `QuitSequencer` owns window close, app quit, system quit, and update relaunch.
  Repeated requests share draft preparation and one service shutdown. An app
  quit arriving during a background-close policy check upgrades that operation
  to a real quit. Preparation retains ownership through renderer resume and
  draft-error dialogs; queued quit requests run after that ownership is released.
- The service publishes `stopping` as soon as shutdown begins. Its public
  readiness is false, new business calls fail as shutting down, and lifecycle
  shutdown/status traffic continues on the existing service session.
- Transcript followers stop without sending subscription-cleanup RPCs once the
  service is stopping. The shared follower owner covers local and remote tabs,
  including late hydration. A delayed response from the old generation cannot
  update the displayed transcript or send cleanup after service stopping.
- Transcript failures are logged with bounded stage, reason, error type, transport,
  revision, commit, attempt count, failure count, duration, service phase, and
  service generation fields. Repeated identical failures emit at most one
  visible summary per 30 seconds; a reason change is immediate. Recovery is
  recorded after a successful delta follow, not merely a replacement snapshot,
  so repeated broken deltas retain their failure count and duration. Repeated
  recovery snapshots do not add breadcrumbs. Missing or failing shell diagnostic
  capabilities cannot interrupt transcript recovery.
  The renderer-to-shell endpoint rejects free text, unknown fields, payloads
  over 2 KiB, untrusted senders, and more than ten accepted events per second.

The rotating `shell.log` is the durable source for these diagnostics. Startup
lines include the shell version, channel, commit, PID, and run generation;
service-ready lines include the service build, PID, and generation; exit lines
include the shutdown request ID, reason, draft-save time, service time, total
time, and result.

Files under `%APPDATA%\reasonix\diagnostics\lifecycle\` are temporary lifecycle
evidence. A successful exit removes the current run's file, and diagnostics can
also be disabled by policy or a development build. An empty directory after a
normal exit is therefore expected. Collect `%APPDATA%\reasonix\logs\shell.log`
and `service.log` for post-exit investigation.

## Follow-up review verification

Review baseline: `a40c1eca5f9c6849ee82c5e027798085d9692519`. Repair commit: `fee5c199b9838e2f1c606f962327f5fb5404a67f`.
The following local macOS checks passed:

| Directory | Commands / evidence |
| --- | --- |
| `desktop/electron` | `pnpm typecheck`, `pnpm test` (235 tests), `pnpm build`; the final log-order adjustment also passed all 22 lifecycle tests |
| `desktop/frontend` | `pnpm typecheck`, `pnpm test:typecheck`, `pnpm build` including unchanged bundle budgets |
| `desktop/frontend` | `pnpm test:transcript`, `pnpm test:app-lifecycle`, `pnpm test:remote` |
| `desktop/frontend` | `pnpm exec tsx src/__tests__/transcript-follow-client.test.ts` (22 tests), `pnpm exec tsx src/__tests__/transcript-session-follower.test.ts` (37 tests) |
| `desktop/frontend` | `node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/remote-session-history-prime.test.tsx` (14 checks) |
| `desktop/frontend` | `pnpm test:app-browser` (Chromium lifecycle, navigation, runtime, and attention checks) |
| Repository | `git diff --check` |

New regressions control draft-error dialogs, renderer resume, delayed baselines,
lazy loading, rejection and retry timing using deferred promises and fake clocks.
They cover both local and remote followers, bounded diagnostics during persistent
delta failure, optional/failing diagnostic hosts, and generation-specific cleanup.
No Go sources changed. Native Windows package qualification remains unexecuted.

## Windows package qualification

Use one final installer or portable build with an isolated `REASONIX_HOME` and
the bundled service. Do not use the development handshake bypass.

1. Confirm strict shell/service handshake and a renderer `Version` call.
2. Exercise repeated title-bar close, Alt+F4, tray quit, background hide, and
   reopen, including overlaps while draft preparation is held by the test
   fixture.
3. Repeat with unsent draft text, an active session, and several tabs; restart
   and verify all durable state.
4. Hold service shutdown with the deterministic fixture and repeat close/quit.
   Verify one request ID and one shutdown transaction.
5. Confirm that the shell and service both exit and no crash overlay,
   unhandled rejection, or residual process remains.
6. Inject local and remote transcript failures and verify the stable stage and
   reason in `shell.log` and copied crash text, correlated by build commit,
   service generation, and absolute occurrence time.
7. Confirm a clean exit may leave the lifecycle directory empty while rotating
   logs remain available.

Until this matrix passes on native Windows, report the status as
“implementation and local tests complete”; do not report the Windows incident
as validated.

## Separate migration investigation

Historical ownership migration remains outside this change. The minimum
redacted model is: three sessions are registered to workspace **A**, while one
pending import operation targets workspace **B**. Investigation must compare
the workspace registry membership/lifecycle records with the migration ledger's
source mapping, target, operation revision, and completion receipt. Do not move,
copy, or delete sessions or pending operations until the intended target is
proven.
