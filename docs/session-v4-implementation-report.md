# Canonical session v4 implementation report

Date: 2026-09-14  
Base: `main-v2` at `744c2e94ec77b577fbe3f1c93c769b1c3388bd21`

## Delivered behavior

- `internal/session` is the single production session service. New and resumed
  writes use `reasonix.session.linear/v4` with `storageRevision: 1`; older
  checkpoints, DAG logs, preview logs, linear v3/v3.1 logs, and the unpublished
  v4 draft are migration inputs only.
- Large fields are published to the SHA-256 content store before their event
  references are accepted. Content is streamed, range-readable, block-verified,
  deduplicated within the authorization domain, and included in export closure.
- Logical commits use bounded, independently verified Zstandard frames and
  begin/event/end transaction records. Accepted and durable watermarks remain
  separate, incomplete transactions remain invisible, and write pressure causes
  cancellable backpressure instead of a session-size rejection.
- Legacy schema-1/checkpoint migration copies and hashes the source as a stream,
  parses messages incrementally, stages imports on disk, validates the target,
  and publishes it atomically. The source remains unchanged on failure.
- Durable history is projected to a rebuildable SQLite index. Desktop, Serve,
  remote transcript APIs, search, large-field expansion, and Goal diagnostics
  use bounded pages or streaming writers rather than whole-history RPC values.
- Runtime memory retains the current business state, accepted tail, and model
  workset instead of all durable message bodies. Export/import, fork, recovery,
  and diagnostics retain fixed-snapshot and execution-authorization rules.
- Goal CAS, armed/disarmed state, unique continuation reservation, user-input
  priority, and pre-side-effect Flush checkpoints are retained from the merged
  Goal lifecycle. Goal rounds have no hidden product ceiling.

## Compatibility result

| Format | Current reader | Current writer | Downgrade behavior | Result |
| --- | --- | --- | --- | --- |
| checkpoint / schema-1 | streamed migration or read-only discovery | never | source bytes remain available | compatible |
| schema-2 DAG | graph-aware migration adapter | never | source graph remains available | compatible |
| preview and linear v3/v3.1 | explicit migration adapter | never | source bytes remain available | compatible |
| draft v4 revision 0 | explicit migration adapter | never | draft remains untouched | compatible migration boundary |
| v4 revision 1 | native | only production format | previous releases do not write this directory | explicit one-way boundary |

The canonical v4 directory is separate from legacy storage. Migration does not
rewrite old data, and cold browse, migration, import, or fork never restores
approval or Goal activation.

## Cache contract

Storage references, hashes, disk paths, cursors, and watermarks do not enter
provider requests. System prompts, tool schemas, message order, reasoning
signatures, provider metadata, visibility, and compaction triggers retain their
existing behavior. The serialized-byte guard covers OpenAI Chat Completions,
Anthropic, and OpenAI Responses before and after a v4 close/reopen cycle.

## Verification evidence

- Root Go suite: `go test -p 2 ./... -count=1 -timeout=15m`
- Desktop Go suite: `cd desktop && go test ./... -count=1 -timeout=15m`
- Race suite: `go test -race ./internal/session ./internal/sessioncontent ./internal/control -count=1 -timeout=15m`
- Exact former replay failure: a `134,308,416` byte legacy log migrates, opens,
  validates, and can continue writing.
- Large-record migration: a single legacy record larger than 64 MiB migrates
  without the former record-size rejection.
- Provider bytes: OpenAI, Anthropic, and Responses serialized request bytes are
  identical after v4 persistence and reopen.
- Capacity run: 100,001 events, 1 GiB logical history, 1 GiB attachments, and a
  16 MiB retained model workset completed. The measured indexed-page P95 was
  597 milliseconds and process peak RSS was approximately 235 MiB on the
  reference macOS host. Cold open completed in 10.1 seconds. Initial index
  construction took 115.1 seconds, so the aspirational 60-second reference-host
  target is not met; index construction remains cancellable and does not prevent
  reopening or streaming access. These are measurements, not admission limits.
- A 256 MiB run measured 153 milliseconds indexed-page P95 and approximately
  230 MiB peak RSS.
- Repository ratchets remain enabled. Their baseline was regenerated because
  the versionless package rename changes path-keyed findings and the new
  migration/query code intentionally increases measured source complexity.

Frontend type/tests, the production frontend build, and native transcript
layout evidence are recorded in the pull request check results.

## Deliberately omitted evidence

Windows installation and packaged-application validation were removed from this
delivery at the request of the task owner. This report does not claim native
Windows filesystem, installer, signing, or WebView2 evidence. It also does not
claim recovery of the original user log because that artifact was not provided;
the exact-size regression above is synthetic.
