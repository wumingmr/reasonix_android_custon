# DSH runtime alignment

This document fixes the executable contracts for the Reasonix runtime
alignment. It is normative for new sessions; legacy fields are display-only.

## Todo contract

`todo_write` accepts one object containing a required `todos` array. Every item
contains exactly `content` and `status`. Content is trimmed and must be non-empty
and unique within the replacement list. Status is one of `pending`,
`in_progress`, or `completed`. The complete array replaces the previous array;
`[]` is a valid clear. Ordering, deletion, replanning, pending-only lists, and
multiple in-progress items are valid.

The successful result is canonical JSON containing the normalized full list and
counts. Validation failure leaves the prior state unchanged. Result truncation
or duplicate presentation must never replace this state-bearing result.

Todo lifetime is one real host turn. A committed top-level turn start clears the
current list. Tool rounds, compaction, steering, approval answers, and Ask
answers stay in the same turn and do not clear it. A completed, failed, or
cancelled turn retains its last successful list for display until the next turn
starts. Goal continuation is a new turn and starts empty.

`complete_step`, hierarchical levels, `activeForm`, `step_id`, host advancement,
approved-plan seeding, Goal restoration, and completed-prefix protection are
retired execution behavior. The hidden `complete_step` tombstone only returns an
actionable retirement error. Old transcript records remain readable.

## Runtime ownership

The session-scoped turn-loop is the execution authority: it uniquely owns
foreground admission, cancellation, the active turn identity, FIFO pending
input, and the level-triggered wake. `session.Runtime` is the persistence
authority: session identity, the single-writer lease, in-memory event receive,
and background write-behind. Durable turn events record facts and never
recreate a live executor after process restart. Runtime phases are `idle`,
`executing`, `cancelling`, `finishing`, `recovery_required`, and `closed`;
compatibility booleans are derived from the phase and current owners. There is
no parallel Activity permit and no second admission gate on `session.Runtime`.

Stop is session-scoped. A supplied UI turn id is diagnostic only and cannot be a
precondition for cancellation. Cancellation signals the bound turn-loop before
storage, notification, or callback cleanup, and each turn uses a fresh cancel
context so Stop cannot poison the next turn. The existing 15-second tool
straggler grace applies to owned work. A turn whose owned work cannot converge
is represented as `recovery_required`; late work cannot resume that turn or
commit a newer runtime generation. The Runtime binds the turn-loop with an
exact generation so an old Controller unbind cannot clear a replacement's
control.

Ask, approval, Plan, recovery, and MCP decisions use `PendingPromptOwner` as one
registry. Every identity binds prompt id, kind, turn, and runtime epoch. Resolve
is single-winner and rejects stale runtime or turn identities. Callbacks run
outside the registry lock. Cancellation drains every registered prompt, and the
runtime snapshot derives `pendingPrompt` from this registry rather than an
approval-only side channel.
Resolution events preserve `answered`, `rejected`, or `cancelled`. A Plan
resolution and its Plan state are one logical batch. Turn termination closes any
remaining requests, so a restart cannot recreate answerable authority.

## Capability catalog

`skill.Store.Snapshot` is the discovery boundary. A generation has immutable,
stable-order candidates and an O(1) name index. Concurrent cold callers share a
single discovery scan; cancelled waiters do not cancel a scan needed by other
callers. Invalidation retains the previous complete generation until the new
one is complete, and refresh retries are bounded to two generations. Create,
update, delete, and detected root changes invalidate the generation.
Windows uses a cancellable bounded polling generation because
`ReadDirectoryChangesW` registration can block teardown; other platforms keep
fsnotify. Both paths cover external edits and missing-root creation.

`use_capability search` consumes one catalog and one MCP schema snapshot. Skill
argument lookup uses the store index and cannot rescan roots per result. List is
paged at 50 entries by default and 100 maximum. Its cursor binds to the catalog
fingerprint; a changed catalog rejects the cursor and requires a restart.
Discovery never connects an MCP server or calls `tools/list`.

## Compatibility and cache boundary

The changed todo schema and capability pagination are one intentional stable
prefix revision. Within the new contract, tool order, JSON schema bytes, and
the delivery marker stay deterministic; runtime state, timestamps, catalog
generations, and todo contents never enter the system prompt.

Legacy todo fields, completion declarations, Goal todo payloads, and dismissal
records are retained only so existing history can be shown. Continuing work
does not activate them. New Goal state writes no todo payload, approved Plans do
not generate todo calls, and the frontend keeps dismissal only as an in-memory
view preference.

## v3 session boundary

`internal/session` defines the ownership-cutover codec
`reasonix.session.linear/v3.1`. The retired `reasonix.session.events/v3`
prototype and `reasonix.session.linear/v3` preview cannot be opened for
execution and must pass through the restricted importer. Unknown required
events, damaged complete records, and unexplained projection operations remain
read-only and cannot resume execution. A session has one active write handle. Fork and edit-resend create an
independent child session instead of adding writable heads to one log. One
physical JSONL record contains one complete logical batch with contiguous event
sequences. Cold reads omit an unterminated tail. After acquiring the exclusive
lease, a writer preserves its original bytes and truncates back to the last
complete batch before restart recovery. Complete malformed records, gaps,
unknown codecs, and unknown required events fail closed.

Three layers own distinct facts. The in-memory `Session` owns the typed event
log, sequence allocation, the operation-idempotency table, and the projection.
`PersistenceBinding` owns the write-behind queue, the durable watermark, and the
single drain chain. The physical `Store` owns the JSONL bytes, the writer lease,
and the rebuildable offset index. The handle exposes no projection, operation
table, or accepted commit list, so business state cannot be reconstructed from
disk layout. `Session.PrepareBatch` copies the payload, validates the schema, and
computes the operation digest before the commit lock is taken; the digest
covers only caller-supplied fields, so retrying one logical batch stays
idempotent. `Session.CommitPrepared` then performs the idempotency check,
sequence assignment, whole-batch append, and projection swap under one short
memory lock. A batch reaches the binding's queue after the commit lock is
released, and that enqueue never performs file I/O.

Create returns only after the in-memory Session, writer handle, and immutable
session ID have been published. `session/title` and `session/config` are the
authoritative title and model-selection facts; the latter includes the
connection revision. Rebuilding an Agent replaces model context and appends
configuration without creating another Controller that competes for the same
writer. Directory indexes and legacy model sidecars are not v3 state sources.

The host owns each published Runtime through a `RuntimeOwner`, which is the only
authority that can terminate an exact instance. `Service.Open` attaches a client
by returning a `ClientBinding` and never hands out close authority, so a failed
attach can only withdraw the caller's own grant. Controllers receive a
`ClientBinding` that may send and observe work, but releasing a tab or connection
cannot close the shared writer or cancel an active turn. The last binding marks an idle
Runtime for retirement; an active Runtime finishes under host ownership and is
retired afterward. Prepare failures and stale cleanup callbacks can discard
only the exact candidate or instance they own. Cancellation reaches the bound
turn-loop without first taking the Runtime or persistence lock, so its receipt
does not wait for a commit, observer, or disk operation. Session event
commits require only that the current Controller still holds the write lease;
Stop does not revoke `history/replace`, `turn/end`, interaction wrap-up, or
diagnostic writes. Session accept is first; the compatibility ledger and
frontend projection advance only after Session receives the batch.

`Append` means the live session accepted a fact. It validates the whole batch,
assigns sequences, retains an immutable copy, updates the in-memory projection,
and publishes to observers. It does not imply durability. The first pending
event starts a fixed 200 ms write-behind window; later appends do not extend the
deadline, and each handle has one drain chain. A background write failure keeps
the original ordered batch and pauses automatic retry. The next explicit
`Flush` retries safely. An uncertain write or fsync result becomes an explicit
uncertain persistence state and never causes a tool rerun.

The agent flushes before every model adapter call and before entering a
top-level tool body. A failed checkpoint prevents the downstream call. Todo,
approval, assistant-message, and `turn/end` appends do not force individual
flushes. Idle is not a durability guarantee. Export, cold-disk verification,
writer handoff, and clean shutdown wait for an explicit flush. Live snapshots
carry both event and durable sequences, and the former may be newer.

The new root is `sessions-v4`. Legacy continuation resolves the transcript and
its paired preview event directory as one import decision, and it classifies
before it publishes anything. It freezes the legacy transcript under its write
lease and the paired preview under directory and writer ownership locks, then
parses both from the frozen copies only. A structured preview wins when the
frozen legacy messages equal its messages or are a strict prefix of them; the
transcript wins when the preview is the strict prefix; divergent work remains
read-only and produces no target at all. Only after the source decision is final
does migration build and validate one v3 session in a same-filesystem temporary
directory and publish it by atomic rename. The canonical source path, head, digest, and target codec determine
the target ID, so identical input is idempotent and changed input creates a new
target. Original artifacts are copied byte-for-byte under `legacy/`; unknown
content is never decoded and re-encoded. Goal import excludes todos and
automatic continuation. Old unfinished runtime and approval records remain
history and never recreate live authority.

Each reachable head in a legacy schema-2 log migrates to a separate linear
session. `legacyHeadId` participates in both the deterministic target ID and the
migration-map key. Catalog listing reads the manifest head, the log revision (stat plus a bounded
head/tail identity sample), and rebuildable scalar metadata only. It never
builds the offset index. Cache entries are pinned to the exact log revision, so
an appended byte invalidates the entry without replaying the log. Missing
metadata is reported as pending and rebuilt with at most two concurrent
streaming reducers. Warm listing reads no event bodies. Cold
history pages validate records as a stream and stop at the requested page
boundary rather than materializing the complete log.

At the final coordinated cutover, Desktop host RPC moves to protocol version 5.
The Electron shell sends and validates the version from its embedded command
contract, so shell and service cannot drift through separately maintained
constants. Serve advertises `execution-v2`, `session-history-v1`, and
`session-identity-v1`, and `session-ownership-v1`. New Desktop builds reject execution control against a
remote missing any capability instead of emulating the new state machine over
old RPCs or path identities.

## Regression matrix

- Whole-list tests cover empty, pending-only, multiple in-progress, out-of-order
  completion, trimming, duplicate content, unknown fields, and invalid status.
- Replay tests must cover equal counts with different statuses, duplicate output
  presentation, compaction, branch isolation, and a new-turn clear.
- Catalog tests use 1,154 and 10,000 candidates and assert scan/read counts:
  exactly one shared cold scan, zero warm scans, and O(1) indexed skill lookup.
- Cancellation tests use channels as ordering barriers for model streams,
  serial and parallel tools, prompt publication, answer/cancel races, hooks,
  discovery, and compaction. Time-based sleeps are not correctness evidence.
- Runtime tests assert prompt registry projection, stale answer rejection,
  session-level Stop without a turn id, and explicit recovery-required state.
