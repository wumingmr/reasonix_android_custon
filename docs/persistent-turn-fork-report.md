# Persistent turn fork implementation report

Date: 2026-09-14
Base: `main-v2` at `236db8c16`

A completed turn can now be forked into an independent child session while its
parent keeps running, while the parent is read-only, and after a restart. The
authority for "this turn ended" is the persisted event log, never a checkpoint:
checkpoints keep their own job of file rollback.

## Delivered behavior

- **Turn boundaries come from the log.** The session projection records two
  facts per completed turn: `MessageID`, the stable transcript identity of the
  turn's final text reply, and `BoundarySequence`, the last sequence of the
  commit that closed the turn. Eligibility is computed after the complete
  closing commit has been projected: the turn must be closed, the boundary must
  be the commit tail, and no interaction or tool authority may remain.
  Completion is never inferred from answer text, elapsed time, or run state.
- **A cut covers a whole commit.** `BoundarySequence` is the closing commit's
  last sequence, not the `turn/end` event's. A turn end and the state that ends
  with it can share one commit, and cutting at the event would either inherit
  half an operation or be refused by the safe-prefix check. Both refusals keep
  their own typed reason (`ErrForkActiveAuthority`, `ErrForkBoundaryNotAtomic`)
  and the commit is never trimmed to fit.
- **One target query for every read path.** `ForkTarget` / `ForkTargetSet` /
  `ForkTargets` / `ForkSequence` answer from committed events only, so a live
  source, a source owned by another process, and a cold read of a closed session
  produce the same targets. The open turn is listed with `available: false,
  reason: turn_open`, which is what lets a surface disable exactly that turn
  instead of every turn while the session runs.
- **Legacy history is refused, not guessed.** A source that keeps messages
  without turn records reports `verifiable: false` and an empty target list.
- **Read-only and cold sources fork.** `Session.Fork` no longer refuses a
  session without a leased writer, and the cold read handle exposes its
  directory, so a child copies the parent's owned files without acquiring the
  parent's writer lease. A parent owned by another process keeps its lease and
  its running task.
- **Creation is separate from navigation.** `Service.CreateFork` publishes the
  child and returns its identity without opening a runtime, switching a
  controller, or writing the parent. `Controller.CreateForkSession` takes no
  rotation gate, so a running parent keeps running. The desktop opens the child
  in a new tab afterwards; when that attach fails, the result still carries the
  child session id and a recoverable error, and the child is never deleted.
- **Unknown results survive restart.** Desktop owns `operationId` and persists a
  pending record in `fork-operations.json` before local writes or network
  requests. Timeouts, disconnects, decode failures, and uncertain internal
  errors retain it. The record becomes completed before success reaches the UI
  and is removed only after the child is adopted. A later intentional click
  then receives a new id and may create another child from the same turn.
- **Remote creation does not take over the parent.** Fenced `GET /fork-targets` and
  `POST /fork-session` are create-only and leave the foreground session, the
  broadcast binding, and the lease untouched. They are advertised as
  `session-fork-targets-v1`; a desktop talking to a server without it reports
  the server as unsupported rather than falling back to `/fork`. The response
  names the authoritative source; create requires `sourceSessionId`, `turnId`,
  `boundarySequence`, and `operationId`, and refusals use structured JSON.
- **The button follows the persisted log, not checkpoints.** The transcript
  matches a turn to its target by stable message identity
  (`ForkTargetView.messageId` against the rendered answer's message id), so live
  completion, paged history, and cold restore share one mapping. Creation carries
  the target's source identity and boundary, so a tab switch cannot reinterpret
  an inherited turn id in another session. Fork no longer
  reads checkpoints or the session-wide running flag; the unfinished turn alone
  stays unavailable while a turn runs. Checkpoints continue to drive file
  rewind only.
- **Every refusal names itself.** The fork entry reports its own state — turn not
  finished, targets still loading, boundary unverifiable, server unsupported,
  stale source, creation in flight — localized in English, Simplified Chinese, and Traditional
  Chinese, and failures reach the existing notice channel instead of being
  swallowed.
- **Legacy paths are unchanged.** `Fork`, `ForkForTab`, `ForkWorktreeForTab`,
  `ForkRemoteTab`, `POST /fork`, and every rewind scope keep their previous
  semantics and rotation protection, including the dirty-worktree checks. The
  switching fork commands remain available to the CLI and to older clients even
  though the desktop UI no longer calls them.

## Compatibility result

The durable conversation format did not change. The v4 log, manifest, frame
codec, and content store are untouched, and the child is written in the current
format. Desktop adds a separate host-owned operation journal, and the rebuildable
recovery projection version advances so old cached projections cannot omit the
new availability field.

| Field or format | Old-data behavior | New-reader behavior | Previous-reader behavior | Conclusion |
| --- | --- | --- | --- | --- |
| `events.frames`, `manifest.json`, frames, `.content-v1` | unchanged | reads as before | reads new writes | no format change |
| `Projection`, `TurnBoundary` (+availability) | durable events unchanged | recomputed from complete commits | old recovery projection v1 is rejected and rebuilt | safe cache invalidation |
| `fork-operations.json` | absent | created atomically on the Desktop host and removed after acknowledgement | ignored | additive host state |
| Host RPC contract | pre-release correction | anchors replace turn-only create arguments; acknowledgement added | capability was not published | safe to correct in place |
| Serve capability set | additive token | advertises `session-fork-targets-v1` | older desktop uses `/fork` | safe |
| Rewind checkpoints (`.ckpt/` sidecars) | unchanged | unchanged; rewind still uses them | unchanged | safe |

Pre-existing inconsistency found while testing, not introduced here and left
as-is: `projectLegacyImport` accepts a `source` field on `legacy/import`, but
`internal/session/history_index.go` decodes the same event under
`DisallowUnknownFields` with only `messages`, so an import event carrying
`source` fails a history-index rebuild. The only production writer emits
`{"messages": …}`, so no current path triggers it; a future writer that adds
`source` would break cold history paging.

## Cache contract

`scripts/check-cache-impact.sh` reports **"No cache-sensitive prompt/tool files
changed."** No provider-visible prompt, memory prefix, tool schema, or provider
request serialization was touched, so no cache-hit warning applies.

The new projection fields are not part of `provider.Message`, and
`ModelMessages` construction is unchanged; `TestProviderRequestBytesSurviveSessionV4RoundTrip`
passes. A child inherits the exact event prefix through the target boundary, so
its model context is the projection the parent had at that boundary, and no UI
anchor, disable reason, or operation id enters a model message.

This report does not claim a cache hit-rate effect for the child's first
request. The verified statement is narrower: the parent's request bytes are
unchanged, and the child's inherited prefix equals the parent's projection at
the target boundary.

## Verification evidence

Independently re-run in the worktree, not only reported by the implementing
agent:

| Command | Result |
| --- | --- |
| `go test ./internal/session ./internal/control ./internal/serve ./internal/servecontract/... -count=1` | ok on the merged final tree |
| `go test ./internal/session -run 'ForkTarget\|CreateFork\|ForkAvailability' -race -count=1` | ok |
| `cd desktop && go test -race -run 'ForkTargets\|CreateFork\|ForkOperation\|ForkedSessionLocator' -count=1 .` | ok |
| `go test ./... -run '^$' && go build ./internal/... ./cmd/...` | ok |
| root and Desktop `golangci-lint run --timeout=5m ./...` | 0 issues |
| `go run ./tools/repolint` | clean (1,230 baselined findings) |
| `scripts/check-cache-impact.sh` | no cache-sensitive files changed |
| `go run ./tools/desktopinventory -check` | current, 761 entries |
| `cd desktop && go test -run 'HostContract\|HostCommandOwners\|HostShellRemote' -count=1 .` | ok |
| `cd desktop && go test -count=1 .` | ok on the merged final tree |
| `cd desktop/frontend && pnpm build` | ok, typecheck and bundle budgets included |
| `tsx src/__tests__/turn-fork-transcript.test.tsx` | ok |
| `node scripts/run-tests.mjs --keep-going` (frontend) | all 360 suites passed |
| Locale parity across `en.ts` / `zh.ts` / `zh-TW.ts` | all 11 `chat.branch*` keys present in each |
| `node bench/fork-targets.mjs` (Chromium, real Transcript) | PASS on the final tree |
| `node bench/fork-targets-app.mjs` (built app, `/?mock=1`) | PASS on the final tree |
| `make lint-cross` | root linux/darwin/windows clean; stopped on four pre-existing unused Desktop linux tray stubs, unchanged from `origin/main-v2` |

The browser bench runs against the real `Transcript` with isolated fixture data
and reads the rendered DOM, not internal state:

- A completed turn renders an enabled entry: `aria-disabled` absent,
  tooltip and `aria-label` read "Branch into a new conversation" in English and
  "在新对话中分支" in Simplified Chinese.
- The unfinished trailing turn renders `aria-disabled="true"` with the reason
  "This turn has not finished yet, so it has no boundary to branch from."
  ("该轮次尚未结束，还没有可供分支的边界。").
- A source whose history keeps no turn records renders the boundary as
  unverifiable ("该轮次在会话记录中没有可确认的分支边界。").
- Clicking dispatches the target's source session, generation, stable `turnId`,
  and boundary. Desktop, rather than the renderer, assigns operation ids.
- In the built app, a click adopts the child tab; with the fixture forced into an
  attach failure, the notice names the created child and a **second click returns
  the same child**. After acknowledgement, another click may create a second
  intentional child.

The session tests cover: a completed turn listed after the controller is gone
and the session is re-opened read-only; a cold fork inheriting only the prefix
through the target turn; an open trailing turn leaving earlier turns forkable; an
unknown turn id refused instead of redirected to the newest turn; a cut covering
the whole commit that closed the turn; idempotent retry per operation id; a
read-only source yielding a writable child with the source log unchanged; a
  boundary whose commit leaves execution authority open refused with its own
  reason and publishing nothing; authority resolved later in the same atomic
  commit accepted; source replacement refused as `stale_source`; operation
  recovery across host reconstruction; and message-only history reported unverifiable.

## Known gaps

- **A fork requested while another rewind is committing is no longer blocked in
  the UI.** That is the intended consequence of dropping the session-wide
  disable; the host refuses it with its own reason instead.
- An imported `legacy/import` event carrying a `source` field would fail a
  history-index rebuild (see Compatibility result). Pre-existing, not triggered
  by any current writer.

## Deliberately omitted evidence

- **Remote fork in a browser.** The remote create path is exercised only by node
  tests (anchored call shape, structured refusal, acknowledgement). No browser
  run covers it, because that needs a live Serve surface to attach to.
- **The two bench gates are not wired into `package.json`.** They are runnable by
  the commands above but do not yet run in CI, so nothing prevents them from
  rotting.

- **Packaged desktop and native shell.** No package was built, signed, or
  launched, so production-mode shell/service startup is unverified.
- **Windows and Linux runtime.** Root cross-platform lint passed for linux,
  darwin, and windows. Desktop linux cross-lint remains blocked by four
  pre-existing unused tray stubs that are unchanged from `origin/main-v2`; no
  packaged application was run on either platform.
- **Live provider calls.** No real-API run was made; the child's inherited
  context is verified against persisted projections, not against a provider.
- **Cross-process lease behaviour under contention.** The read-only path is
  designed to take no lock and the tests cover cold reads after close, but no
  test drives two live processes contending for one session.

## Fixture finding worth keeping

The locale switch in the bench was initially asserted synchronously and failed
about once in seven runs. The cause is a property of the app, not of the fork
work: `src/lib/i18n.tsx` loads a locale dictionary on demand and `translate`
falls back to English until the chunk resolves, so the first render after a
switch legitimately shows English. Measured apply latency was 12-98 ms across
ten runs, so nothing is lost or stuck — the page simply never promised a
synchronous switch. The bench now waits on the rendered value
(`page.waitForFunction`, 10 s bound) instead of sleeping, and passed on the
final tree. Any future browser check that reads localized text must do the
same.
