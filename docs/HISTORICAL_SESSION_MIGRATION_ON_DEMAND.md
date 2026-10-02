# On-demand historical session migration

## Scope

Desktop startup no longer converts every legacy transcript or canonical v4
directory. Startup only repairs small, non-historical lifecycle reservations.
The historical catalog reads directory entries, published head indexes, and
durable registry metadata. Legacy and canonical v4 rows participate in the normal sidebar and
history search; opening one starts preparation in the conversation navigation
flow and switches to the canonical target only after preparation commits.

Restored, unprepared tabs retain their title and a non-error **Import and open**
action. Merely restarting does not prepare content or start a controller. The
action uses the same navigation owner as sidebar/history selection. Empty
canonical probe directories are not historical sessions; damaged session
artifacts remain discoverable for an explicit retry.

The management surface is **Settings → Storage → Historical sessions**. Trash
contains archived/deleted canonical sessions only.

## Ownership and lifecycle

- A source import takes a per-source cross-process lock and, for canonical
  stores, a shared directory ownership lock for the complete copy and commit.
- A source already owned by another CLI or runtime returns a blocked source
  status immediately. It never waits behind the desktop runtime rebuild lock.
- Duplicate requests for one source join the same revisioned preparation task.
  Interactive navigation and a bulk batch hold separate demands, so cancelling
  a batch cannot cancel a session that the user is currently opening.
- Batch import is sequential, cancellable, and resumable. Its selected source
  snapshot is stored in `historical-import-queue.v1.json` with an atomic,
  cross-process-locked update. After restart it is paused until the user
  explicitly continues. Cancellation leaves
  `prepared` and `content_ready` reservations for the next explicit attempt.
- Existing `content_ready` operations are replayed against their durable target;
  they are not converted into a second session. Recovery validates the target
  and lifecycle fences without requiring the old source or its ownership lock.
- Archive and purge state remains authoritative. Deleted or archived sessions
  are not resurrected by a later catalog scan.
- Once adopted, the source and its canonical target share one conversation
  identity. The sidebar displays only the canonical row; archive hides both
  aliases and creates one trash entry, while restore returns only that row.
- Permanent deletion keeps adoption and topic-removal evidence. An explicit
  purge records source-cleanup intent before removing content and may also
  remove an unchanged canonical source directory once no active, archived,
  pending-import, or unresolved recovery owner references it. Shared sources,
  changed content, legacy multi-file DAG originals, and shared content pools
  remain intact. A missing historical root does not block canonical deletion.
- `PrepareSession`, `GetSessionPreparation`, and
  `CancelSessionPreparation` expose `queued`, `preparing`, `ready`, `blocked`,
  `failed`, and `cancelled` scheduling states without adding lifecycle phases.
- Source content checks compare durable bytes rather than timestamps or title
  metadata. A confirmed version can be explicitly imported with
  `PrepareHistoricalSourceVersion`; its `:review:<fingerprint>` mapping is a
  separate branch while the original mapping remains stable.

Discovery publishes a metadata snapshot in the background; ordinary lists only
read that snapshot. Renames and pins are applied before search and sorting, and
transferred on adoption. A pending sidebar source follows the same navigation
preparation owner as history. Cancellation responses are fenced by navigation
intent, operation identity, and revision; branch preparation cannot take focus
back after the user navigates elsewhere.

Shutdown drains the batch worker before releasing queue ownership, preserving
the current and remaining selections for manual continuation. One process owns
a batch worker lease. Sidecar mutations read the latest disk value under the
write lock, modify only their owned fields, and atomically replace it; stale
queue revisions are rejected rather than overwriting another process's work.

## Compatibility

Discovery, import, archive, and restore leave source files unchanged. Explicit
permanent deletion follows the guarded cleanup rules above. Source mappings,
recovery entries, and unknown fields remain durable; deleted sessions retain
minimal adoption evidence after live presentation and membership are removed.
Path normalization aliases resolve older source hashes without rewriting their
receipts or pending operation identities. Independent head and reviewed-version
identities remain separate; ambiguous normalized ownership is rejected. The
path-only legacy route remains an alias for the selected DAG head; valid head
indexes expose alternate heads as separate on-demand sources. A missing or
stale index degrades to one source row and never causes event-log replay in a
listing RPC.

The queue sidecar is scheduling intent only. The workspace lifecycle registry
remains authoritative for target Session IDs and commit state. Older builds
ignore the sidecar and optional RPC fields; surviving committed sessions and
retained sources remain readable after rollback. Explicitly purged content is
not restored by rollback. Older purge journals without source-cleanup intent
never gain authority to remove originals on upgrade. Preparation metadata is never added to
model prompts or transcript messages.

| Data | Compatibility behavior |
| --- | --- |
| Lifecycle registry | No new operation types or phases; existing target IDs and mappings remain authoritative. Optional versioned cleanup intent uses the purge request payload. Older readers may leave originals behind but cannot restore the deleted canonical identity. |
| Version 1 queue sidecar | Optional `queueRevision` defaults to zero; existing files load paused. Unknown root and presentation fields survive writes. |
| Rollback to pre-sidecar builds | Scheduling is unavailable; retained source files and surviving canonical sessions remain readable. Purged content remains deleted. |
| Concurrent older sidecar writers | Older code does not honor the new worker lease/revision contract; do not run mixed-version batch writers. Upgrade all Desktop instances first. |

## Verification

The implementation has deterministic coverage for startup non-migration,
cross-process cold-export contention, duplicate and cancelled imports,
prepared/content-ready resume, revisioned duplicate preparation, paused queue
restart, archive/purge fencing, and browser interactions for listing, retry,
open-after-commit, and batch controls. Regression tests additionally cover normal
global/project discovery while a source is occupied, title/pin search and adoption,
shutdown during commit followed by restart, independent sidecar writers, unknown
field preservation, source-independent recovery, and late navigation responses.
Lifecycle regressions cover older source hashes across restart, one trash entry,
independent heads, shared-source protection, source-writer contention, missing
historical roots, and process exit before and after source cleanup.
