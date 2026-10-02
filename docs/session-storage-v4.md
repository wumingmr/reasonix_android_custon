# Reasonix session storage v4

Status: production write format for `main-v2`. The implementation lives in
`internal/session`; older formats are migration inputs only.

## Identity and layout

```text
<data-root>/sessions-v4/
  <session-id>/
    manifest.json
    events.frames
    legacy/                 # immutable migration evidence, when present
  .content-v1/              # immutable SHA-256 addressed objects
  .query-cache/<session-id>/history-v1.sqlite
  .migration/
  .trash/
```

The accepted manifest tuple is:

- `schemaVersion: 4`
- `codec: reasonix.session.linear/v4`
- `storageRevision: 1`

A v4 manifest without `storageRevision: 1` is an unpublished draft and is
accepted only by the explicit migration adapter. Controllers and clients pass
a host-scoped `SessionRef`; paths and codec versions do not enter business
interfaces.

## Commit and frame contract

Each logical commit is encoded as `batch/begin`, one or more `batch/event`
records, and `batch/end`. Every record is an independent CRC-enabled Zstandard
frame with the `RX4F` header, compressed length, and decoded length. The end
record authenticates the ordered begin/event records with SHA-256. Readers
publish a commit only after the complete end record validates.

The encoder and decoder share an 8 MiB frame budget. Event payloads above 64
KiB are published to the content store before their references can be
accepted, so user content size is not a frame or session limit. The 16 MiB
write-behind budget applies cancellable backpressure and charges a large batch
only a bounded hot-memory amount. It is not a maximum batch or history size.

Accepted and durable watermarks are distinct. Provider calls and side-effect
tools must cross `Flush`; an uncertain append is compared byte-for-byte with
its disk staging evidence before it can be confirmed or retried.

## Content and history

Content objects preserve exact bytes and use SHA-256 identity. A separate
1 MiB block-integrity index supports verified range reads. A content reference
is readable only after the session history index proves that the reference is
part of that session's durable view. Export copies the complete referenced
object closure and rewrites the archive to a self-contained `.content-v1`.

The SQLite history database is derived data. It is rebuilt from the log after
loss, version drift, or a log revision change. History pages:

- start at the newest durable messages;
- move toward older messages with an opaque cursor;
- bind the cursor to session identity, storage revision, projection version,
  and durable snapshot sequence;
- return at most 500 messages and approximately 2 MiB of encoded data;
- return a preview plus `ContentRef` for a large message.

Search uses the same fixed-snapshot cursor rules and returns previews, never
full bodies. Service-owned runtimes retain only the accepted UI tail and the
current provider model projection; durable UI message bodies are read through
the history service.

## Compatibility matrix

| Source | Browse | Continue | New writes |
|---|---|---|---|
| checkpoint / schema-1 event log | read-only adapter | streamed into v4 | v4 only |
| schema-2 DAG | read-only adapter | selected head imported into v4 | v4 only |
| prototype v3 | read-only adapter | explicit import into v4 | v4 only |
| linear v3 / v3.1 | read-only adapter | explicit import into v4 | v4 only |
| unpublished v4 revision 0 | migration only | explicit import into revision 1 | v4 revision 1 only |
| v4 revision 1 | native | native | native |

Schema-1 and checkpoint messages are streamed through a one-turn normalization
window into a private disk spool. Replace records truncate that spool; append
indexes are checked against the raw source count. This removes the former 128
MiB cumulative replay gate from migration without removing it from untrusted
interactive legacy replay. Schema-2 still uses its graph-specific compatibility
reader so head, patch, redaction, and fork semantics are preserved.

## Recovery rules

- Final v4 at the canonical `BranchID` always wins over an older paired legacy
  checkpoint. It must never be reinterpreted as a preview format.
- A partial final frame or batch is uncommitted. The writer preserves evidence
  before truncating to the last complete batch.
- Unknown required events, a complete corrupt frame, a digest mismatch, or a
  divergent legacy/sidecar pair fail closed.
- Cold browse, migration, import, and fork never restore approvals or arm a
  Goal. Restart recovery closes active tools/interactions and ends the turn as
  interrupted; it never repeats a side effect.
- Failed migration and import leave the source unchanged. Targets are built in
  sibling staging directories and published by atomic rename.
- Shared content is intentionally not garbage-collected in revision 1. Deleting
  a session cannot invalidate a fork or exported archive.

## Resource limits versus product limits

There is no accumulated byte, message, event, or Goal-turn product limit.
64 KiB, 1 MiB, 2 MiB, 8 MiB, and 16 MiB values govern placement, transfer,
allocation, and backpressure. Disk exhaustion, permission failure, invalid
input, or a single provider request that cannot fit the provider work budget
remain explicit errors; none permits deleting or silently truncating history.

## Capacity acceptance

The production-path capacity runner emits JSON timings, disk usage, Go memory
high-water marks, and process peak RSS on Unix. Its default is a quick smoke
run. The release-scale data set is explicit and therefore cannot become a
runtime admission limit:

```sh
go run ./tools/sessioncapacity

go run ./tools/sessioncapacity \
  -root /absolute/path/to/evidence/sessions-v4 \
  -history-messages 50000 \
  -history-bytes 1073741824 \
  -attachment-bytes 1073741824 \
  -workset-bytes 16777216
```

Each history message contributes a `message/complete` and bounded
`model/context-replace` event; the final workset event makes the full command
slightly exceed 100,000 events. Attachment objects use a deterministic stream
without allocating an attachment-sized buffer. The runner closes, cold-opens,
rebuilds the history index, verifies the retained model workset, and measures
indexed newest-page latency. Keep the stated `-root` for release evidence; an
omitted root is deleted after the run.
