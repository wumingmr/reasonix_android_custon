# Transcript projection

[简体中文](TRANSCRIPT_PROJECTION.zh-CN.md)

Local Desktop and current Serve sessions share `internal/transcript`. The
controller appends each durable envelope, commits its display projection, then
publishes the event outside the commit lock. Planner output, executor output,
prompts and terminal events use this boundary. Provider history remains the
input to model requests; display records never enter those requests.

The Desktop surface uses the Electron host contract. New transcript methods
and ownership metadata are generated together; DTO names cannot shadow the
TypeScript helpers used by that contract.

## Identity and recovery

The controller reserves a user message ID before planning. The frontend keeps a
local submission echo outside the durable transcript until the canonical row is
installed. `submissionId` correlates one send attempt, `messageId` identifies the
durable message, and the presentation key only preserves a mounted DOM node
during handoff. Durable user rows always use `m:<messageId>`; history mutation,
content lookup, and persistence never use the presentation key. Equal bodies are
allowed and are not duplicate identities. A sampling attempt reserves its
assistant message ID before its first delta; successful persistence uses that ID,
and discard removes only that attempt's owned records. Tool cards use tool call
IDs.

Local submission handoff is independent of the resident history window. The
follower sends formal user identities before window filtering alongside the
visible projection in one reducer action. A record outside a reader's window
retires its matching echo without inserting a row or requesting tail follow.
Matching prefers an already bound message ID; only unbound echoes may match a
submission ID. Conflicting bindings and different formal message IDs never merge.

The same transaction records `visibleSubmissionHandoffs` for formal users in the
resulting window. ChatSource uses these hints to inherit a previously mounted
echo's key even when React skips the intermediate binding render. User, process,
and tail nodes share that stable turn key. Hints are pruned to resident formal
users; presentation maps are pruned to mounted groups. Neither survives session
replacement or restores a reclaimed echo. A missing send anchor is not permission
to prepend an echo to unrelated history: placement requires its original boundary
or explicit turn identity.

A late message-ID binding first reconciles resident records. If the formal user
is no longer resident, the owning follower may use the existing exact-message
history query solely to confirm identity, without installing that page. Reads
are generation-fenced, coalesced per unresolved submission, and retried only after
committed coverage advances or reconnect. An inconclusive read retains the echo.
An identity event alone never starts a read: committed coverage must have advanced
since the follower first observed the unresolved submission. This watermark is
pruned with the submission. The synchronization engine loads on first follow,
and stopping before its module loads cancels that start.
RPC acceptance, failure, and unknown outcome belong to the local submission;
request ownership separately controls runtime state. Only a new explicit send
advances the scroll submission revision.
Snapshots retain unresolved submissions for the same canonical session ID;
a different snapshot owner clears echoes and handoffs and advances the session
generation. Remote callbacks read the state owned by their tab, never another
tab's last rendered state.

History window and body I/O load on demand. Their routing identity is captured
before the deferred import, and Store generation checks still discard stale
completions. This keeps startup bytes within the existing bundle budget.

`bench/submission-handoff.mjs` exercises the real follower, bounded Store, reducer,
Composer, and Transcript using 1,000 deterministic turns and 20 paging round trips.
Run it with `--electron` for the isolated Electron host. Its JSON evidence includes
DOM continuity, native selection, reader displacement, viewport writes, resident
entries, and presentation-map sizes. Scripted Electron input is not OS-native input
qualification; the report records that evidence separately.
In Linux CI, `--electron --native-input` runs inside Xvfb and uses X11 XTest
wheel, keyboard and scrollbar input through `xdotool`. The evidence includes
scroll extents and blank-frame samples as well as confirmation-time reader
stability. Run this mode only in an isolated graphical session.

Terminal projection checkpoints live in the session's
`.transcript-projection.json` sidecar. The checkpoint records the provider prefix
count and digest, session head/rewrite identity, rows, runtime, and covered event
sequence. A failed checkpoint write keeps the WAL projection acknowledgement
pending. Recovery restores a matching checkpoint and replays its retained
suffix without executing tools. It does not seed an autosaved in-flight tail
and then append that same tail again. Existing display sidecars remain readable;
only their legacy migration path may use the old user-hash/occurrence mapping.

Terminal records retain protocol-recovery tokens, incomplete-read/readiness
details, accepted partial-read receipts, cancellation and failure diagnostics.
Starting a new turn retires earlier recovery actions. Checkpoint capture copies
mutable metadata while sharing immutable body strings; disk encoding stays
outside the projection lock.

## Snapshot protocol

Desktop exposes `TranscriptSnapshotForTab`, `TranscriptPageForTab`,
`TranscriptContentForTab`, and `TranscriptReplayForTab`. Serve exposes GET
`/transcript/snapshot`, `/transcript/page`, `/transcript/content`, and
`/transcript/replay` behind its existing authentication and host checks.

`ResumeTranscriptSessionForTab` and `OpenChannelTranscriptSessionForTab` return
switch-phase diagnostics after adoption, without building a legacy history page.
The frontend records these only after the matching snapshot commits, including
a separate snapshot-install duration. An older host returning no diagnostics is
reported as unknown (`duplicateLoadCount: null`), never as proof of zero repeats.
Legacy page callers reuse a preload only if it matches the controller's captured
history digest; a changed, fully persisted runtime triggers a fresh durable read.

Each snapshot includes protocol version, snapshot ID, session/head/rewrite/runtime
identity, projection revision, covered-through sequence, records, active owners,
and pending runtime prompts. Page and content requests carry the same snapshot
ID. Replay carries the identity and the last committed sequence. An expired cut
returns `stale`; a replay identity or retained-range mismatch requires a new
snapshot. Tab and remote-client generations are checked after asynchronous reads.

The frontend suspends ordered ingress before requesting the snapshot. Mutable
owners' referenced prefixes resolve before rows, runtime and attempt state commit
in one reducer transaction. Only then does event coverage advance. Queued and
replayed events share one commit entry. Status polling supplies replay hints and
ancillary job counts; it cannot install a second transcript/runtime prefix.

Older pages merge by record/item identity and backend order, including an active
user retained before the newest page. Delayed content patches check both the cut
and intervening item mutations. A page cannot resurrect a discarded attempt.

Content resolution uses durable message identity. The presentation layer may
carry a local echo's key across canonical handoff, but that key never replaces
the item's `m:<messageId>` identity. Delayed patches target the current durable
item and reject intervening mutations before replacing its preview; a reclaimed
row cannot be recreated by a late patch.

## Bounds and compatibility

Pages default to 120 records and 512 KiB, with a 2 MiB response ceiling. Large
string fields use 4 KiB previews and UTF-8-safe 64 KiB content chunks. Chunk reads
traverse typed fields directly instead of serializing the entire payload.
The same 2 MiB limit covers complete replay responses, including JSON escaping
and envelope overhead. Oversized replay pages request a fresh snapshot; the
covered data remains accessible through content chunks without advancing an
unreceived event cursor.
Snapshots retain at most three cuts under a 64 MiB estimated budget. The current
cut is pinned: a larger session remains readable and evicts older cuts. Settled
strings are shared, while immutable metadata and active prefixes are retained.

The client retains unresolved previews only; successful expansion releases that
record cache. Active/running tabs are pinned. Inactive caches have a three-tab,
32 MiB budget. Live gap buffers have a 1,024-event/8 MiB bound; overflow retains
the high-water mark and recovers the suffix from the ledger.

An old Serve returning an unsupported endpoint/protocol or its HTML index stays
connected through the legacy path. The UI states that synchronization is limited
and reconnects may have missing or repeated content. Capability discovery never
upgrades or restarts Serve. Modern hydration uses metadata-only ancillary reads,
so it does not also download the old full `/history` payload.

## Validation

Root-module tests cover attempt identity, checkpoint write failure, autosave
recovery, immutable cuts, nested content references, large sessions, and HTTP
session binding. Desktop tests cover planner cancellation, old-Serve discovery,
late remote responses, and metadata-only reads. The frontend snapshot client
tests exercise prefix/suffix races, optimistic keys, page ordering, tool pairs,
discard tombstones, and stale content writes through the real reducer.

Run the root and Desktop Go suites separately. Run frontend `test:typecheck`,
`test:transcript`, `test:remote`, `test:stream`, and the production build. Browser
transcript and app-memory checks, together with native Electron replay,
remain separate acceptance gates; unit tests or cross-compilation do not replace
native evidence.
