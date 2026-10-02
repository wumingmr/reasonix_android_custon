# Session Resource Governance Upgrade

> Baseline: `main-v2` @ `2553bb31c`, merged on 2026-09-15
> Scope: Desktop history, streaming Markdown, tool details, body reads, media release, and skillwatch diagnostics

## Motivation

The CPU and memory reports against 1.38.8 point to a resource-ownership problem, not one isolated hotspot. History, the live tail, Markdown parsing, detail bodies, and media decoding could grow under separate rules. Full-history materialization, active sessions bypassing page limits, repeated full-source streaming parses, and worker respawn churn are source-confirmed risks. Their individual contribution to the field report still requires sampling the native 1.38.8 package; source inference is not a performance baseline.

This refactor adopts the useful DeepSeek Harness principles: explicit owners, reclaimable but reachable data, typed capability outcomes, interaction-priority scheduling, and bounded content-free diagnostics.

## What changed

### Bounded bidirectional history

- Each session retains three adjacent pages of 32 entries by default, including active sessions.
- Readers can load older, load newer, or return to latest. Reclamation happens at the edge farthest from the reader.
- Reclamation returns stable item IDs that the UI must unmount. Tool calls and results are released as one ownership unit.
- Reclaimed data remains cursor-reachable. Invalid cursors return typed `stale_cursor` and re-anchor instead of driving control flow through error-string matching.
- Native selection inside the transcript blocks paging, preventing removal of nodes being copied.
- After `turn_done`, the live tail is reconciled to the latest durable bounded page, closing the former unbounded active-tail path.

### Shared budgets

Defaults live in `desktop/frontend/src/lib/resourceBudgets.ts`:

| Resource | Default | Overflow behavior |
| --- | ---: | --- |
| History bodies | 32 MiB/process | weighted LRU of inactive sessions |
| Resident history | 3 × 32 entries/session | reclaim far edge; keep cursors |
| Body reads | 4 global, 2/session | queue and deduplicate; cancel on release |
| Markdown AST | 12,000 elements/publication page | preserve whole top-level blocks; load more |
| Large Markdown table | 2,000 cells/page | load more inside the table |
| Tool preview | 16 KiB and 64 blocks | UTF-8/surrogate-safe preview; full value remains readable |
| Tool relations | 20/page | load more in details |

The 12,000-element value is a progressive publication target, not a deletion threshold. One indivisible top-level AST block remains whole even when it exceeds the target; large plain tables have a separate cell-page budget. Full copy uses the parse-time selection projection rather than only mounted DOM.

### Stateful Markdown worker protocol

The worker now follows `open -> append* -> replace* -> finalize -> release`.

- Prefix growth transfers only the suffix; replacement and finalization carry authoritative snapshots.
- Superseding a live parse drops stale output without terminating and recreating the worker.
- Scheduling priority is interactive stream, visible history, then background history.
- Worker-unavailable environments retain the asynchronous in-process fallback.
- Parsing stamps block fingerprints and element counts. Rendering compares integer identities instead of serializing every old and new block.
- The parser intentionally remains whole-document for reference and footnote correctness. The protocol removes thread churn and repeated bridge transfer; it does not claim incremental grammar parsing.

### Export, details, and media

- Local Markdown export writes on the host through a 64 KiB buffer and atomically publishes only after flush, sync, and close. The renderer no longer assembles a complete local-session string.
- Export is routed by tab ownership. Older remote protocols retain the renderer fallback, which can export only the currently loaded window.
- Audio and video release decoder/network ownership on URL change or unmount using `pause -> remove src -> load`.
- Tool previews, related-call pages, and full-body reads have explicit shared limits and cleanup.

### Diagnostics

Runtime Doctor and Desktop diagnostics expose physical/logical skill watchers, scans, degraded mode, and detailed counters. Session diagnostics expose resident entries, page limit, and reclaimed pages. Metrics are bounded and content-free: counts, sizes, timings, and closed status labels only.

## Compatibility

- Routing is chosen from binding identity. A peer without `history-window-v1` returns `unsupported` and stays on protocol 7 older-only paging; the client does not fake newer paging with a full download.
- A failed local or remote read never falls through to the other identity source.
- Legacy `HistorySlice`, one-shot Markdown `parse`, and remote export remain for one compatibility cycle.
- Reclamation affects memory and DOM residency only; durable history is not deleted.

## Verification and remaining evidence

Local gates cover Go session/serve/skill/boot packages, race tests for skillwatch/session, the complete Desktop Go module, frontend production/test typechecks, transcript/pretest suites, hook lint, the single-scroll-writer contract, app-layer checks, and repolint. Targeted tests cover 10,000-turn reachability, three-page residency, live-tail reclamation, call/result ownership, 4/2 read concurrency, preview/detail budgets, worker lifecycle and priority, decoder release, and atomic host export.

The browser fixture passed on its first run on 2026-09-15 using macOS arm64 and Chromium 153. At 240 turns it measured 129.3 ms initial load, 2040.7 ms deep paging, 1173.6 ms streaming publication, 24.1 ms input p95, and 12,090 DOM nodes. The 1000-turn compatibility stress case measured 147.6 ms, 23175.6 ms, 4987.7 ms, 45.9 ms, and 47,834 DOM nodes respectively. Session-switch p95 was 44 ms, measured JS heap growth was 349,380 bytes, and anchor drift was zero. The 1000-turn full-page DOM count also demonstrates that the 12,000 budget is enforced per Markdown publication page, not as a global transcript hard cap.

The production frontend build passed every bundle budget, and `desktop/ go test ./...` passed (the main package took 335.895 s). Root `go test ./...` had only three pre-existing `cmd/reasonix-legacy-migrator` failures, all reporting `pending update belongs to a different installation: pending update bundle is not the current Guard installation`; the remaining root packages touched by this change passed.

Still required before a performance release claim:

- Native 1.38.8 CPU/RSS/heap/DOM/polling baseline has not been captured, so no trustworthy before/after percentage is available.
- macOS browser/Electron evidence cannot substitute for Windows WebView2, Linux WebKit, or installed-package validation.
- The Windows polling/full-mount contribution remains source inference until measured on the real package.
- The local Markdown destination is streamed, but a compatibility controller may still materialize a complete `HistoryForTab` slice first. This removes the renderer full-string peak; it does not make every legacy storage path constant-memory.

Before release, run the same long-session fixture against 1.38.8 and the candidate package: cold start, session switching, a 15-minute stream, deep bidirectional paging, and export. Record main/renderer CPU, RSS, JS heap, DOM nodes, worker creations, and peak read concurrency. Any unreachable cursor, lost selection, incomplete export, or sustained growth on any platform should block the release rather than relax a budget.
