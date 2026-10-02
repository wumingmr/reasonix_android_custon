# Immutable list and search reads

[中文](READ_SNAPSHOT_PAGINATION.zh-CN.md)

## Problem and repair boundary

A local Desktop session could report `session_operation:stale_cursor:The session list changed. Reload it.` while its owner was writing titles/results. A double metadata collect required every writer to remain idle. Removing that collect fixed first-page starvation but still allowed live re-sorting to invalidate subsequent pages. The frontend could miss the failure when its first logical window required multiple 200-row RPC calls. This error does not establish a failure of SSH, Git, Docker or remote Reasonix.

The read result is now owned by `desktop/read_snapshot_store.go`. Continuations read immutable rows by ordinal. They do not re-sort current data or compare global catalog revisions. This is a read contract; commands still resolve current authoritative ownership and lifecycle.

## Entry points

| Surface | Implementation |
| --- | --- |
| Local project/global/group/query lists | `session_workspace_sidebar.go`, `session_topic_index.go` |
| Pinned shells | Same snapshot pages, released after collecting the complete pin list |
| Compatibility project tree | Same pages; `ListProjectTree` returns an RPC error instead of successful partial children |
| Legacy history metadata | `history_read_snapshots.go` plus one `sessioncatalog.WithReadView` |
| Legacy full-text and exact-target search | Shared frozen candidate/snippet builder; `historycatalog.CaptureSearch` streams one ranked SQL result |
| Project list recovery | `projectTreeWindow.ts`, `ProjectTree.tsx` |
| Paired history list/search recovery | `useHistoryCatalog.ts` |

Canonical transcript/history-window mechanisms and persisted session/provider formats are unchanged.

## Read protocol

1. An empty cursor starts or joins a build for a query binding. The binding includes the API kind and target/filter/sort parameters, excluding page size.
2. Capture membership, presentation, organization and lifecycle from one registry projection; read each canonical session's metadata once. Nested legacy catalog reads share one SQLite read transaction.
3. Freeze the selected rows and their order. Search captures candidates before loading source content. Each snippet and its content digest come from the same source read; mismatches request reindexing and mark coverage partial. I/O errors fail the build.
4. Publish only a finished result. Each caller receives a distinct opaque release handle, even when concurrent callers shared a build.
5. Version-1 cursors contain a random process-local handle, query binding and next ordinal. Repeating a cursor returns the same frozen page. Unknown old-version/restarted/evicted handles are rejected; they are never interpreted as live offsets.
6. Ordinary writes and organization edits leave existing results readable. A fresh query sees updates. Relevant archive/delete/move/adoption or source replacement invalidates its dependent result.
7. `snapshotId` and `snapshotExpiresAt` are additive response fields. History APIs retain `staleCursor` and add `readError`. RPC errors retain `stale_cursor` and add a structured `readReason`. `ReleaseReadSnapshot` is idempotent.

Metadata lists validate catalog/registry ownership without opening transcript files. Search also fences file identity, and context requests carrying `contentDigest` refuse changed content before interpreting message indexes.

## Resource and lifetime policy

| Limit | Default |
| --- | --- |
| Stored handles, including shared-build cache leases | 64 per App |
| Accounted resident rows and retained dependency metadata | 32 MiB |
| Concurrent builders | 2 |
| Spill threshold per result | 512 KiB of accounted data |
| Private SQLite storage | 256 MiB total; reserve up to 64 MiB per file |
| Single captured result | 64 MiB |
| Idle / absolute cursor lifetime | 10 / 30 minutes |
| Public page size | At most 200 |

Storage reservations include unpublished builds and intermediate search candidates. SQL/file work and lifecycle validation execute outside the store management lock. A per-result read lease protects storage from disposal. A cancelled waiter does not cancel other waiters; App shutdown cancels builds and disposes files. Temporary files live in newly created private directories; cleanup never scans another process's directories.

Expiry is enforced on access and expired entries are reclaimed by subsequent builds or shutdown. Limits describe accounted snapshot storage, not a whole-process RSS ceiling: SQLite caches, registry projections and a single source replay have their own transient working memory. Disk reservations are conservative, so four spilling results/candidate files can exhaust the disk allowance before physical disk use reaches 256 MiB. Exhaustion is an explicit failure, never truncated success.

## Frontend publication and refresh

- A logical window stages all its RPC pages before committing. A stale internal second page is handled the same as a stale outer continuation.
- One automatic rebuild is permitted per logical operation. Rebuilding an append replaces the complete visible window; it never appends fresh rows to an older snapshot. A second failure retains the current display with a retry path.
- Mixed IDs, nonadvancing cursors and empty pages with a continuation fail explicitly.
- Background events coalesce over a fixed 200 ms window. Pending reads complete; events queue one follow-up. Background refresh starts are separated by at least 500 ms per logical list. Explicit query/sort changes retire the previous generation immediately.
- Expanded size, stable row identity and selection survive refresh. The project list restores its visible row anchor without overriding an intervening user scroll. Superseded snapshots are released; obsolete responses cannot release the current generation's handle.
- History metadata and body hits stage and commit as one UI generation. Tool parts participate in hit identity, avoiding collisions between same-message tool inputs.
- Project, current-session, query and filter changes immediately isolate old rows/cursors and retire old button callbacks, previews and search contexts. Background failures retain results only within the same identity.
- Removing and re-adding a project never reuses request generations. An obsolete completion cannot consume a newer generation's queued refresh. Invalidation, window reset, project removal and archive release retired snapshots.
- History lists and ordinary searches bind the captured active-session identity to prevent sharing a build across a focus change. Explicit-target searches remain owned by their selected source and survive unrelated foreground changes.

## Verification and delivery gates

Deterministic regressions cover 205/405/1000-row windows, ongoing metadata writes, organization changes, archive fences, cross-query cursor rejection, fixed ranking during unrelated index writes, source deletion, spill/expiry/resource rejection, independent releases and shutdown. Frontend tests cover paired recovery, retained errors, pending refreshes, query/sort invalidation and malformed pagination. The Chromium `independent-sessions` scenario includes the three large-window sizes.

Run root catalog/history package tests separately from the Desktop Go module, focused `-race`, generated-host-contract checks, frontend typecheck/lint and the browser scenario. The pre-existing `TestLargeTranscriptSwitchPhaseMeasurement` hang has been reproduced on the original HEAD. An exploratory broader run excluding it still exceeded the ten-minute suite budget; there is no full-suite pass. Run contract generation and its freshness check sequentially so regeneration during a running test cannot produce a misleading stale-artifact failure.

Before release, verify the packaged macOS/Windows app with a local running conversation, SSH connected, an expanded list above 200 rows, legacy body search, archive/restore, and shutdown. Browser/local Go evidence does not establish native-package acceptance. No Git push, release or production deployment is part of this local implementation.
