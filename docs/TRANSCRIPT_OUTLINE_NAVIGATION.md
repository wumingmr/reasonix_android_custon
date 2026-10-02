# Turn outline and cross-page navigation

[简体中文](TRANSCRIPT_OUTLINE_NAVIGATION.zh-CN.md)

> **Historical acceptance record — superseded by PR #10385 (Follow v2).**
> This document records PR #10276's complete-outline implementation and the
> validation performed at that time. Its before/after comparisons and results
> are not acceptance evidence for current production wiring.

Current behavior uses a bounded bidirectional history window and a loaded-turn
rail; complete persisted history remains accessible through canonical
search/locate. See [Transcript v2](TRANSCRIPT_V2.md), the
[scroll and history contract](TRANSCRIPT_SCROLL_CONTRACT.md), and
[Natural-flow chat](TRANSCRIPT_ARCHITECTURE.md). The historical design and
validation below are preserved for traceability.

## Reported problem

In a long, tool-heavy conversation the first body page holds the newest 120
records. When that window contains fewer than two user questions, the rail hid
entirely: the reader had to press "load earlier messages" repeatedly before any
navigation appeared. Switching to another conversation and back rebuilt the
body window, so the rail could disappear again, and markers were renumbered
from 1 because the ordinal was derived from the loaded window. A turn outside
the window could not be reached at all.

| | Before | After |
| --- | --- | --- |
| Rail content | Loaded user turns only | Every turn of the conversation |
| First paint, long session | Hidden until history was loaded manually | Complete, without loading any body |
| A → B → A | Rail reset with the rebuilt window | Rail restored from the snapshot index |
| Ordinal | Renumbered from 1 per loaded window | Absolute turn number of the conversation |
| Unloaded turn | Not listed | Listed, previewable, clickable |
| Clicking it | Not possible | Pages history in, then scrolls to it |

## Design

- `internal/transcript` publishes a bounded turn outline beside the body pages,
  bound to the same immutable snapshot, revision, and event coverage. Entries
  carry the user record's stable `RecordID`, optional `MessageID`, absolute turn
  ordinal, the record's `order` inside that snapshot, and two previews built
  from display bodies only: 50 grapheme clusters of the prompt and 120 of the
  last non-empty assistant body in that turn's group. Reasoning, tool output,
  submitted text, and injected context never enter an entry.
- The index is built once per frozen cut, so repeated reads reuse one pass, body
  paging never shrinks it, and its previews count against the existing snapshot
  cache budget and lifetime.
- `GET /transcript/outline` serves it, advertised to remotes as
  `transcript-outline-v1`. `TranscriptOutlineAPI` is an optional capability
  beside `TranscriptProjectionAPI`, so an existing controller keeps compiling
  and an unsupported route answers 404/405/501 rather than an empty page.
- The frontend keeps one shared outline store for local and remote sessions,
  fenced by tab generation and snapshot identity. A recycled cut is reported as
  stale; the body is only replaced when the reader explicitly retries.
- Selecting an unloaded turn starts a jump transaction that reuses the ordinary
  older-history paging one page at a time, waits for the progressive mount to
  advance, and writes the viewport only once the target node is really mounted.
  Reader intent, an explicit cancel, a newer target, or a session/snapshot
  replacement ends the pending transaction without taking scroll control back.

## Verification

Code and automated checks are complete for the local and remote paths. Platform
verification is listed separately below and is not claimed beyond what ran.

### Go (ran)

```sh
go test ./internal/transcript/ ./internal/control/ ./internal/serve/
go test -race ./internal/transcript/
cd desktop && go test ./...
```

Covers: outline completeness for a tool-heavy tail whose body page holds no
user turn; identity shared with body records; absolute numbering and survival of
older paging; staleness for a recycled cut; preview bounds, whitespace
collapsing, Unicode safety, and exclusion of reasoning/tool output; empty
prompts keeping turn identity; byte budget and cursor advance; the response
limit; past-end offsets; an empty session encoding `entries` as `[]`; the HTTP
endpoint's paging, session binding, capability advertisement, and 501 for a
controller without the capability; and outline reads racing streaming commits.

### Frontend (ran)

```sh
cd desktop/frontend
pnpm test:transcript      # includes transcript-outline-store, chat-turn-jump,
                          # chat-turn-outline-jump
pnpm test:remote
pnpm build                # typecheck, scroll-writer gate, CSS/theme, bundle budget
```

`transcript-outline-store` covers multi-page assembly, duplicate identity,
stale cuts, a stalled cursor, absent capability versus a real failure, and
release fencing of an in-flight read. `chat-turn-jump` covers mount-confirmed
paging, an unproductive page, exhausted history, reader preemption,
supersession by a newer target, session replacement, explicit cancel, and the
bounded mount wait. `chat-turn-outline-jump` drives the real `Transcript` in
jsdom: the complete rail renders while only the newest turns are loaded, older
turns are marked unloaded, clicking one pages history until its node mounts,
numbering is unchanged by that paging, and the busy state clears.

### Browser (ran)

```sh
cd desktop/frontend
CHAT_BROWSER=chromium node bench/chat-transcript.mjs
```

Confirms the production transcript fixture still renders, streams, and scrolls
with the navigation change in place. Both scenarios in that run reported zero
errors. Recorded on 2026-09-14, arm64 darwin, Chromium 153.0.8010.12:

| Metric | 240 turns | 1000 turns | Gate |
| --- | --- | --- | --- |
| Input P95 | 46.5 ms | 137.6 ms | ≤ 200 ms |
| Switch P95 | — | 38.2 ms | ≤ 300 ms |
| Longest task | 53 ms | 218 ms | ≤ 500 ms |
| Heap growth | — | 0.33 MiB | ≤ 20 MiB |
| Anchor / prepend drift | 0 / 0.09 px | — | no drift |
| Mounted DOM nodes | 12089 | 47833 | bounded rail |

It is a regression check of the real page, not a substitute for the native
platform runs below. The 1000-turn figure also shows the rail does not create a
mark per turn: marks are rendered only for the visible range.

### Windowed application (not run here)

The Electron bench (`node bench/transcript-layout.mjs --electron`) and a packaged
Desktop build require a signed application bundle and were not exercised in this
environment.

### Windows (not verified here)

The reported path was not replayed on Windows in this environment, so no Windows
build SHA can be recorded. Replaying the original scenario on a real Windows
desktop build remains an open external verification item and is not claimed by
this record.

## Compatibility

The change is additive. A client without the capability keeps the loaded-turn
rail and does not claim complete navigation. No persisted session format,
provider message, tool schema, permissions, or prompt-cache byte changes, so
there is no prompt-cache impact. Rollback restores the previous frontend and
ignores the extra read-only endpoint.
