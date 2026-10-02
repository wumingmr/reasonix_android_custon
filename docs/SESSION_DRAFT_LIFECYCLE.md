# Local Session Draft Lifecycle

Reasonix Desktop treats a manually opened local conversation as an editor draft
until the first execution is durably accepted. Opening a draft does not create a
Session, Topic, Controller, session lease, MCP runtime, or session-list row. Once
the first submission creates the formal Session, a temporarily untitled row uses
the localized new-session title (and then the normal preview/title projection);
internal canonical routes such as `session-id:...` are never display names.

## Identity and storage

The visible conversation surface is either a draft target (`workspaceId`,
`draftId`) or a formal Session target (`SessionRef`, `tabId`). Drafts never use a
fake tab or Topic ID.

Draft state is stored in `desktop/drafts-v1.sqlite` under the Reasonix user state
directory. Schema version 4 contains active drafts, versioned frozen submission operations,
conflict copies, and the last restorable draft surface. There is one active draft
per local Workspace, including one independent global Workspace draft.

Persisted content includes composer text, structured invocations, attachment and
workspace references, pasted blocks, selected text references, Session
references, model provenance plus a compatibility mirror, effort/mode/approval/
quality settings, Goal settings, and MCP selection. Runtime objects, credentials,
preview URLs, pending paste state, and submission UI flags are not persisted.

Edits synchronously update an application-level store and are saved with a 250 ms
debounce and a per-DraftID revision compare-and-swap loop. Navigation never waits
for a failed or conflicting save: the source draft continues in the background
and remains marked dirty/error/conflict in the project tree. Submission waits for
the captured source edit version, and normal Electron shutdown waits for every
loaded draft, pending attachment task, and restore-target write. Failure or
conflict cancels normal close; the last backend-confirmed revision remains the
crash-recovery boundary.

Opening draft data and committing the page restore target are separate writes.
Every navigation claims the shared navigation intent before its first await, so
a stale draft open cannot install a page or overwrite the next startup target.

## First execution

The renderer captures DraftID, Workspace, generation, content, settings, and
navigation intent before context expansion. The backend records that immutable,
versioned snapshot before creating runtime state:

```text
active draft
  -> reserved (OperationID + SessionID + TopicID + SubmissionID)
  -> starting (create/open exactly that Session and bind its Workspace)
  -> dispatching | dispatching_shell
  -> accepted (durable submission receipt)
  -> converted draft
```

The operation does not call `EnsureBlankTab`. Retries reuse its reserved Session
and Topic. A terminal retry gets a new OperationID and SubmissionID but retains
the already reserved Session and Topic. Workspace pending-create cleanup is
conditional on the old OperationID so a late failure cannot erase a newer claim.

Normal turns, structured invocations, initial Goals, and shell commands share
this path. New shell submissions persist an acceptance receipt before command dispatch. An unknown shell or
model dispatch result is never replayed automatically.

An untouched new draft inherits the current settings default live. The persisted
`modelSource: "default"` marker distinguishes that inheritance from an explicit
picker or `/model` choice; its concrete `model` field is only a compatibility
mirror for previous readers and is excluded from editable-draft identity. A
catalog/settings refresh updates the visible effective model without making the
draft dirty. An explicit choice writes `modelSource: "explicit"` and remains
pinned.

Frontend and backend draft comparisons both exclude the inherited concrete model,
including lost-save-acknowledgement recovery and pre-submit validation. Catalog
refresh, save, and reopen reads share version fencing so late responses cannot
replace a newer effective-model projection.

The first submission resolves the effective inherited model again, validates and
canonicalizes it, and freezes it in snapshot v5 before Session reservation.
Frozen and explicitly selected draft models are strict execution choices: an
unavailable model is reported before reservation and never falls back to another
model. Plugin model references are passed to the runtime resolver and fail there
when unavailable. A retry applies the operation's frozen settings to the same
reserved Session identity.
Request fingerprints are computed before model-alias normalization, so retrying
the original request still identifies the same operation. Restoring a resumable
operation shows its frozen model in both the composer and picker; cancellation
or terminal failure releases editing to inherit the live default again.

The renderer switches to the formal Session only after the acceptance receipt
and draft conversion are committed. Transcript snapshot/follow then supplies any
events that arrived before the RPC response. Completion in a background project
updates project state but does not steal the visible surface.

## Recovery and cancellation

Sending synchronously locks the source editor, then saves its captured version
before expanding references. A request ID and source digest bind that snapshot
to the durable operation. Lost Begin responses are reconciled by reading state;
they do not unlock the editor or allocate another submission. Restoring a draft
also restores its operation, including the explicit Continue action. Reads do
not create a Controller or dispatch work. Polling continues for unknown results.

Cancellation first records `cancel_requested`; editing remains locked until the
worker has stopped. A cross-process execution lease prevents another process
from declaring a live worker interrupted. A separate short publication lease
serializes cancellation with Controller publication. Retry configuration applies
to the real Controller under turn admission, and admission checks its identity.

Attachment completion always settles its task registration, even if generation
has changed. Only current-generation results may edit content. Discard captures
the project and versions before confirmation; navigation cannot retarget it.
Normal exit also waits for local submission preparation to persist its operation.

| Persisted phase or interruption | Recovery behavior |
| --- | --- |
| No operation record | Keep the editable draft; submit normally. |
| `reserved` or `starting` after restart | Mark `resume_required`; user continuation reuses the same identities. |
| Session exists but is not yet bound | Bind the same Session to the recorded Workspace. |
| Controller startup failed | Keep the Session identity; a terminal retry reuses it. |
| `dispatching` with a known receipt | Atomically accept and convert the draft. |
| `dispatching` without a known receipt | Mark `dispatch_unknown`; do not replay. |
| `dispatching_shell` after interruption | Reconcile the durable receipt; accepted commands become sessions. A legacy dispatch marker without proof remains unknown. Never rerun the command automatically. |
| `accepted` but draft still active | Complete conversion without redispatch. |
| Session was archived or deleted externally | Stop recovery and report the lifecycle conflict. |

Cancellation is keyed by OperationID. Before dispatch it conditionally cancels
the operation and its matching Workspace reservation. At or after durable
acceptance it becomes cancellation of the formal Session; it never deletes a
receipt to pretend execution did not happen.

## Commands, attachments, and capabilities

- `/new`, `/clear`, `/compact`, and other history-dependent management commands
  are unavailable in a draft and do not create a Session.
- `/model` and `/effort` edit draft settings. `/theme` changes appearance
  directly. Goals, skills, custom commands, and ordinary text start the unified
  first-execution flow.
- Configured MCP servers are projected from Workspace configuration without a
  Controller or process launch. Draft selection is persisted per Session;
  runtime capability availability is revalidated during Controller startup.
- Attachment operations use an explicit composer target and Workspace root.
  Async completion writes directly to the captured DraftID even when another
  project is visible. Pending saves block submission. Reads reject path escapes and symlinked files
  or attachment directories. Missing files remain repairable draft references.

## Compatibility

### One-time legacy empty-session cleanup

The first upgraded startup freezes a versioned set of default-title Sessions
and legacy Topic placeholders that already belong to registered local or global
Workspaces. The background worker runs only after Session migration, draft
operation reconciliation, and tab restoration. Identities created after that
freeze, including draft reservation and retry Sessions, can never enter the
batch.

A candidate moves to Trash only when the complete canonical or legacy artifact
set proves it has no user, assistant, or tool message; accepted model or shell
submission; Goal; pinned context; inbox item; job; checkpoint; recovery state;
derived relationship; active draft operation; runtime; or unsent restored UI
state. An empty-string message is still usage. System-only initialization and
empty derived containers are not usage. Corrupt, inaccessible, unsupported, or
future-version data is retained as `unknown` rather than treated as empty.

Default titles are matched exactly after trimming. Sessions with content keep
their title, order, identity, and data; no numbered-title migration is applied.
Conclusive empty Sessions use the existing reversible archive lifecycle without
opening a fallback Controller. Topic-only placeholders keep a schema-v1 recovery
snapshot of their original scope, title, order, pin, and group position and are
shown in Trash without fabricating a SessionRef. Restoring either form preserves
its identity and permanently excludes it from this cleanup batch.
Busy or unreadable candidates stay visible in their original project. Trash
shows the pending count and a **Recheck** action that retries only the frozen
batch; it never discovers identities created after the upgrade boundary.

The independent
`desktop/legacy-empty-session-cleanup-v1.json` sidecar has atomic writes,
cross-process worker ownership, stable operation IDs, and strict unknown-version
protection. Older releases ignore it. Legacy JSONL migration sources are retained
even when their mapped canonical Session is archived.

| Format or API | New-reader behavior | Previous-reader behavior | Result |
| --- | --- | --- | --- |
| Session v5 | Unchanged identity, events, and transcript protocol. | Unchanged. | Compatible. |
| Workspace registry v3 | Formal Session ownership only; recorded create operations remain recoverable. | Existing Sessions remain readable. | Compatible. |
| Draft SQLite v1 | Migrated to v2 by adding the reserved Topic ID, then to v3. | Not applicable to releases without drafts. | Forward migration covered. |
| Draft SQLite v2 | Transactionally upgraded to v3. Pending operations resume only when their draft revision proves the settings match. | A v2 build refuses to write v3. | No inferred execution settings. |
| Draft SQLite v3 | Versioned frozen request and settings in `request_json`; all saves use CAS. | Releases without drafts ignore the independent file. | Rollback preserves drafts for a later upgrade. |
| Draft SQLite v4 | Transactional migration adds request identity, source digest, operation revision and separate expanded execution bytes. Snapshot versions remain independent of schema versions. | v3 refuses to write v4. | Original fingerprints and Session/Submission IDs remain intact. |
| Draft model provenance / snapshot v5 | New drafts persist an additive `modelSource`; inherited defaults keep a concrete compatibility mirror but freeze the current effective model only at submission. Untouched legacy revision-1 drafts are safely marked inherited; edited legacy drafts retain their concrete model because provenance is ambiguous. Existing v3/v4 operation snapshots remain resumable. | Previous readers ignore `modelSource` and use the valid concrete mirror. They reject a future v5 operation snapshot rather than resuming it with changed semantics. If a previous reader edits and rewrites a draft, a later upgrade conservatively treats the concrete model as pinned. | No explicit user choice is silently replaced and no operation is resumed with an inferred model. |
| Unknown future draft schema | Read/write is refused and the file is retained. | Not applicable. | No silent downgrade overwrite. |
| Upgrade-time blank Sessions | Only frozen, default-title, conclusively unused local candidates are moved to Trash once. Content-bearing or uncertain Sessions are unchanged. | Older releases ignore the cleanup sidecar and continue to read formal Sessions and Trash. | Reversible lifecycle-compatible cleanup. |
| Legacy cleanup sidecar v1 | Records frozen identities, decisions, stable archive operations, placeholder recovery metadata, and restore protection. Unknown versions are retained and cleanup stops. | Ignored. | No downgrade overwrite or candidate rescan. |
| Legacy runtime APIs | `EnsureBlankSurface` and related APIs retain their semantics. | Unchanged. | Remote, IM, automation, recovery, and worktree flows remain isolated. |

Draft IDs and submission IDs are host metadata only. They are excluded from
provider-visible requests, and the existing prompt/tool ordering is unchanged.

## Operational notes

Diagnostics record IDs, phase, reuse/recovery counts, and durations for draft
open/save, Session creation, runtime readiness, and acceptance. They do not log
draft text, attachment contents, or credentials.

The first implementation deliberately does not delete attachment files when a
draft is discarded. MCP prompt discovery uses existing configured/cached
capability data; actual capability discovery and validation occur at execution.
