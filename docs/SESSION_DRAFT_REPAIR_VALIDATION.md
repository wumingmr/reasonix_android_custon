# Session draft repair validation

This report covers the local draft repair on top of `8e2e6ef0`. The checkout also
contains the preceding draft implementation; no commit, PR, CI run, or release
was created by this task. [中文](SESSION_DRAFT_REPAIR_VALIDATION.zh-CN.md)

## Repairs

| Defect | Ownership change | Regression evidence |
| --- | --- | --- |
| Retry changed only displayed settings | Prepare or replace the actual Controller under runtime and turn barriers; compare identity and effective profile before admission. Candidate failure preserves the previous runtime and Session. | `draft_runtime_test.go`: real permission change, canonical model/effort/MCP replacement, failed candidate preservation. |
| Editing raced submission capture | Synchronous preparation ID locks content and settings before any await. Flush checks the captured snapshot, and Begin checks its digest. | `session-draft-ownership.test.tsx`, `session-draft-store-races.test.tsx`, Composer regressions. |
| Discard or late completion retargeted navigation | Capture DraftID, generation, edit version, revision, and navigation intent; backend deletion uses CAS. | Target-bound confirmation and late discard tests; navigation fence tests. |
| Old-generation attachment work never settled | Global task settlement is independent of content write eligibility. Async field patches merge into the original owner. | Ownership tests cover conflict replacement and task settlement; native file drop crosses the preload boundary. |
| Restart lost operation identity | Read-only state includes the original operation; explicit resume uses its revision and transfers worker ownership without a lease gap. | Store request/resume tests, restart/no-replay tests, v3 frozen-snapshot regression. |
| Unknown dispatch stopped polling | Application-level polling continues for shell dispatch and unknown acceptance; operation revision prevents rollback. Receipt conversion is idempotent, including accepted cancellation. | Deferred shell polling and accepted-cancel tests; durable shell receipt/deduplication test. |

Additional boundaries repaired: lost Begin response retries the same request ID
under a backend request lock; cancellation is serialized with runtime publication
and durable admission; a prior worker that is still unwinding cannot leave a new
operation silently stuck. Normal exit joins attachments, preparation, saved
versions, and restore writes. Terminal test cleanup waits for Controller-owned
stores to close before deleting their directory.

## Validation scope

- Local frontend: application lifecycle, draft ownership and store races,
  Composer/clipboard, remote, transcript, application/test typechecks, hooks,
  layering checks, and production bundle checks.
- Local Go: Desktop is tested as its own module. Store and runtime ownership
  have focused race checks; root tests cover shared Controller/session changes.
- Native Electron: 219 shell tests, isolated real-service opening, native PNG
  clipboard storage and keyboard paste through Composer, native file drop, confirmed attachment recovery, immediate
  close after a renderer edit, and Electron SIGKILL followed by service restart.
  These tests make no model request. Clipboard fixture contents are temporary.
- Browser: project plus, top-level new, keyboard shortcut, project context menu,
  and command palette all restore the same draft. Twenty repeated new actions preserve it
  without adding session rows. Thirty samples start from a history page and end
  when the correct draft is editable. Latest mock-browser p95: **28.9 ms**.

## Evidence limits

Final local results:

| Gate | Result |
| --- | --- |
| Root `go test ./...` | Passed; shared control package 367.7 s. |
| Desktop `go test -timeout=25m ./...` | Passed; Desktop main package 1054.4 s. Subsequent small recovery/store changes also passed focused checks. |
| Draft store and runtime `go test -race` | Passed, including final unknown-version byte-preservation check. |
| Generated host contract and desktop inventory | Passed freshness checks. |
| Frontend typechecks, hooks, layering, production build | Passed. |
| Draft, Composer, lifecycle, sidebar, remote, transcript regressions | Passed; Composer draft suite 118 checks and clipboard menu suite 15 checks. |
| Electron shell tests | 219 passed, zero failed or skipped. |
| Browser and native smoke | Passed within the scope above. |
| Live CI / Windows and Linux native qualification | Not executed. |

The browser number is a local mock benchmark, not a production Electron latency
claim. Native multiwindow UI conflict interactions and the complete fault-injection matrix were not all
exercised end to end. Deterministic store and component tests cover selected
cross-window and asynchronous ordering cases, including an independent-process
worker lock check. Windows and Linux native clipboard/drop behavior was not
tested on this macOS host. Live CI was not run.

The initial broad runs exposed stale generated files and a teardown cleanup
race; both were corrected. An initial root run also exceeded the existing
60-second unlimited-Goal stress-test deadline under concurrent load; its isolated
run passed in 37.4 seconds. No deadline or product assertion was weakened.

See [lifecycle and compatibility](SESSION_DRAFT_LIFECYCLE.md) for schema v4,
snapshot-version independence, rollback behavior, and recovery actions.
