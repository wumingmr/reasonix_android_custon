# Natural-flow chat transcript

This is the sole production chat renderer, replacing TranscriptKernel, the window adapter and the measurement ledger. Rendering reference: local DeepSeek Harness `c291e7961a`. Reasonix keeps its controller, storage, composer, approvals and workbench; current synchronization and history behavior follow [Transcript v2](TRANSCRIPT_V2.md) and the [scroll and history contract](TRANSCRIPT_SCROLL_CONTRACT.md). The complete turn rail uses durable indexed summaries and direct history-window positioning.

## Ownership

```text
Local / remote Follow v2 → shared consumer and bounded record store
                               ↓
                     Transcript session adapter
                               ↓
          ChatSource: stable order + independent node/status subscriptions
                               ↓
       ChatNodeList → ChatNodeSeat → message / process / tool / notice / tail
                               ↓
                       native document flow

DOM resize + reader intent → ChatScrollController → TranscriptViewportWriter
Full content references → ChatContentLoader → bound session content APIs
Markdown source → shared worker → stable prefix blocks + mutable streaming tail
```

- `src/lib/chatViewSource.ts` owns a reconstructable projection, not another event log. Keys derive from existing message, call and user-turn identities. Unchanged order/node snapshots retain references. Structural publication coalesces in a microtask; stream updates use existing controller frame batching and match the live ID. Settlement replaces the same assistant host.
- `src/components/Transcript.tsx` adapts local and remote hosts. Lists subscribe only to order; seats subscribe to themselves and their process disclosure. Status/timers, navigation and drawer have separate subscriptions. Session replacement disposes subscriptions, queued publications, loader leases and observers.
- `src/components/ChatNodes.tsx` renders messages, thought/process disclosures, compact tools, notices, compaction, extensions and turn actions. Closed heavy bodies are not mounted.
- Loaded history remains in natural flow. There is no chat virtual window, absolute row positioning, resident/cold handoff, size ledger, geometry-state feedback, logical selection overlay, renderer override or safe-mode remount. TanStack remains for unrelated consumers.

## Product behavior

The column is at most 800 px wide, with 24 px horizontal padding (16 px in narrow chat containers). Existing typography/themes apply. Native selection and scrollbars are used. History uses a bidirectional bounded window, retaining three adjacent pages of 32 messages by default. Paging beyond the budget reclaims the opposite end. Navigation describes all visible durable turns using paged summaries; body residency remains bounded.

## Complete-turn navigation and history access

The rail reads `history-outline-v1` summaries from the canonical history index, not the bounded Follow projection. Each page pins storage generation and snapshot sequence. A directory keeps at most six pages of 128 entries under a shared 8 MiB renderer budget; the rail renders only visible marks plus four marks at each end. Eviction preserves the total turn count and re-fetches previews on demand.

Unloaded turns resolve to stable message IDs and request one newer-direction, 32-message window beginning at that message. The common history store owns replacement, paging and request fencing. No intervening pages are loaded. Navigation waits for mounted content and routes all viewport writes through ChatScrollController. New selections, reader input and session replacement cancel obsolete commits and positioning. Stale cuts re-anchor once by message identity, never by an ordinal that may now describe another message.

Follow invalidates directory summaries on committed message changes and durable coverage changes, not token deltas. Directory refresh does not restart Follow or replace the body. Peers without the optional directory capability retain loaded-turn navigation with an upgrade hint; the mandatory transcript-v2 requirement remains unchanged.

This restores complete navigation after #10385's loaded-only product choice while retaining its bounded body window. The earlier #10276 acceptance record is historical; its accumulated-history jump implementation is not restored.

## Full content and asynchronous ownership

History still uses existing page cursors. Prepend projects new nodes and repairs turn boundaries; replacement rebuilds from the current authoritative source. Snapshot revision, session generation and stream-attempt ownership remain in existing layers.

`ChatContentLoader` limits each mounted session to four active requests. Equivalent item content shares a promise; changed source content does not reuse an older request. Disposal fences queued/in-flight results. Completed requests are removed instead of forming an unbounded full-text cache.

User/answer bodies resolve automatically; thoughts/tools resolve on full-disclosure or copy requests. Snapshot reads are field-selective, so reading an answer does not eagerly load its thought. Tool details read detached raw immutable records, bypassing Item preview/archive limits. Fetched tool bodies are not patched into the controller or retained in the cut. Small references and inline tool records remain subject to the existing inactive-cache budget, allowing reopening. Stale cuts use the existing reload path and expose retryable errors.

The legacy history store no longer starts whole-page reference prefetches outside the loader budget. Its tool reader resolves references by call ID without expanding sibling calls or caching their full bodies. An unresolved or stale body reference cannot fall back to a successful copy of its preview.

The UI checks source identity before accepting full content. Drawer closure, target changes and session replacement invalidate old callbacks. Worker parsing checks message/text revision and mount lifetime. `surfaceCommitToken` readiness follows the correct initial DOM commit and two animation-frame opportunities; stale effects are canceled.

## Scrolling and rendering

`ChatScrollController` holds follow intent, stable node key/viewport offset, preceding keys, native offset and task epochs outside React. `TranscriptViewportWriter` is the only direct chat scroll writer, enforced by the static gate.

Initial entry follows latest; revisiting restores bounded in-memory position. Even a small upward wheel movement releases follow inside the 24 px bottom tolerance. Touch, scroll keys and scrollbar input acquire reader ownership. Downward arrival within 24 px resumes follow. Return-to-latest and a new running user turn explicitly resume follow.

Prepend, disclosure, image/Markdown layout and input-area height changes use the stable node and offset. A disappearing process child falls back to its summary; a removed node falls back to a surviving previous node or the first node. User scrolling during pagination updates the anchor. There is no scrollHeight-difference compensation.

One ResizeObserver observes the column, viewport and mounted nonempty node hosts. A MutationObserver refreshes the observed host set. Both coalesce into an animation frame; neither publishes geometry into React nor writes synchronously in ResizeObserver delivery. Browser auto-anchoring is disabled. Unchanged writes are no-ops; settled content must stop producing writes.

Streaming and final messages share MarkdownHistory. Worker parsing reuses stable prefix blocks and changes the mutable tail. Final parsing resolves references, footnotes and incomplete syntax without replacing the whole answer. Parsing failure is isolated to a copyable raw fallback and lightweight notice. Tables have natural rows and horizontal overflow; long code uses disclosure, never vertical virtualization.

Within the resident history window, offscreen completed source text stays mounted as plain text until worker formatting activates near the viewport. Parsed blocks remain mounted while their page remains resident. Page reclamation releases the associated rows and parsing work; lazy formatting does not imply indefinite retention of previously loaded text. Closed process bodies deliberately unmount.

Worker clients are leased by mounted sessions. Last release terminates pending tasks; aggregate diagnostics retain numbers only, not source or AST data. Disposal releases observers, source listeners and full-content results. Existing bounded caches own inactive history.

## Compatibility and verification

The current Desktop/Serve protocol boundary is `transcript-v2`; compatibility and derived-index changes are described in [Transcript v2](TRANSCRIPT_V2.md#compatibility-and-change-notes--兼容与变更说明). Persisted session-log encoding, provider messages, tool schemas and prompt-cache bytes remain unchanged. Old persisted display preferences cannot select an old renderer. Any rollback must keep Desktop and Serve protocol-compatible; a frontend-only rollback is not a general compatibility guarantee.

Run from `desktop/frontend`:

```sh
pnpm test:transcript
pnpm test:stream
pnpm test:composer
pnpm test:remote
pnpm test:app-lifecycle
pnpm test:motion
pnpm test:typecheck
pnpm build
node scripts/run-tests.mjs --keep-going
CHAT_BROWSER=chromium CHAT_EXPANDED=1 CHAT_SOAK_SECONDS=60 node bench/chat-transcript.mjs
CHAT_BROWSER=webkit node bench/chat-transcript.mjs
CHAT_BROWSER=electron node bench/chat-transcript.mjs
node bench/transcript-layout.mjs
node bench/transcript-layout.mjs --electron
node bench/composer-transcript-stability.mjs
node bench/run.mjs
```

Point `PLAYWRIGHT_BROWSERS_PATH` at the installed browser cache when needed. Chat replay uses the real Transcript, Markdown and Composer in a production fixture. The whole-app benchmark additionally uses actual application composition with the existing mock transport. Neither substitutes for a live backend/native-IME soak.

Gates remain input P95 ≤200 ms, switch P95 ≤300 ms, longest task ≤500 ms and released heap growth ≤20 MiB. See [measured acceptance evidence](CHAT_REFACTOR_ACCEPTANCE.md), including unverified platforms.
