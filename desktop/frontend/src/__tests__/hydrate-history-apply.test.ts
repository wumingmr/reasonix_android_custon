// Run: tsx src/__tests__/hydrate-history-apply.test.ts

import {
  canAdoptUnboundLiveSurface,
  duplicateLiveItemIds,
  hasCachedLiveTurn,
  hydratedHistoryApplyMode,
  sameSessionHydrateIdentity,
  sameSessionPlaceholderItems,
  shouldPreferResidentHistory,
} from "../lib/hydrateHistoryApply";
import { activeTabHydrationPlan, coldHistoryRefreshProof } from "../lib/coldHistoryRefresh";
import type { Meta, TabMeta } from "../lib/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

console.log("\nhydrate history apply");

const mode = hydratedHistoryApplyMode;

ok(mode(true, true, false, { items: [] }) === "skip", "skipHistory blocks apply");
ok(mode(false, false, false, { items: [] }) === "skip", "missing projection blocks apply");
ok(mode(false, true, false, { items: [] }) === "replace", "idle empty surface applies history");
ok(mode(false, true, true, { running: true, items: [] }) === "replace", "running empty surface applies history");
ok(
  mode(false, true, true, { running: true, live: { text: "partial" }, items: [] }) === "replace",
  "a mid-stream surface with no rows yet applies history",
);
ok(
  mode(false, true, true, { running: true, items: [{ kind: "user" }] }) === "prepend",
  "a running turn with no history page behind it gets one prepended",
);
ok(
  mode(false, true, true, { running: true, historyTotalTurns: 3, items: [{ kind: "user" }] }) === "skip",
  "an already-hydrated running transcript is left alone",
);
ok(
  hasCachedLiveTurn({
    running: true,
    historyTotalTurns: 2,
    items: [{ kind: "assistant", streaming: true }],
  }),
  "streaming assistant counts as a cached live turn",
);
ok(
  !hasCachedLiveTurn({ running: true, items: [{ kind: "assistant", streaming: true }] }),
  "a live turn with no history page behind it is not cached",
);
ok(
  duplicateLiveItemIds(
    [{ kind: "user", id: "h1", text: "ask" }],
    [{ kind: "user", id: "l1", text: "ask" }, { kind: "assistant", id: "l2", text: "" }],
  ).join(",") === "l1",
  "a live row the page already carries is dropped",
);
ok(
  duplicateLiveItemIds(
    [{ kind: "user", id: "h1", text: "ask" }],
    [{ kind: "assistant", id: "l2", text: "" }],
  ).length === 0,
  "a live tail the page does not carry is kept",
);
ok(
  sameSessionPlaceholderItems({ sessionPath: "a.jsonl" }, { meta: { sessionPath: "b.jsonl" }, items: [{ kind: "user" }] }) === undefined,
  "foreign session items are not placeholders",
);
ok(
  (sameSessionPlaceholderItems({ sessionPath: "a.jsonl" }, { meta: { sessionPath: "a.jsonl" }, items: [{ kind: "user" }] }) ?? []).length === 1,
  "same-session items stay placeholders",
);
ok(
  sameSessionHydrateIdentity(
    { sessionPath: "a.jsonl", sessionGeneration: 3 },
    { sessionPath: "a.jsonl", sessionGeneration: 3 },
  ),
  "same path and generation prove the same session",
);
ok(
  !sameSessionHydrateIdentity(
    { sessionPath: "a.jsonl", sessionGeneration: 4 },
    { sessionPath: "a.jsonl", sessionGeneration: 3 },
  ),
  "different generations reject placeholders even when paths match",
);
ok(
  !sameSessionHydrateIdentity({ sessionPath: "" }, { sessionPath: "" }),
  "empty identities cannot prove the same session",
);
ok(
  sameSessionHydrateIdentity(
    { session: { hostId: "local", sessionId: "canonical-a" }, sessionPath: "", sessionGeneration: 3 },
    { session: { hostId: "local", sessionId: "canonical-a" }, sessionPath: "", sessionGeneration: 3 },
  ),
  "matching SessionRefs prove a canonical session even when both paths are empty",
);
ok(
  !sameSessionHydrateIdentity(
    { session: { hostId: "local", sessionId: "canonical-a" }, sessionPath: "" },
    { session: { hostId: "local", sessionId: "canonical-b" }, sessionPath: "" },
  ),
  "different canonical SessionRefs never share an empty-path surface",
);
ok(
  !sameSessionHydrateIdentity(
    { session: { hostId: "local", sessionId: "canonical-a" }, sessionPath: "same.jsonl" },
    { session: { hostId: "local", sessionId: "canonical-b" }, sessionPath: "same.jsonl" },
  ),
  "SessionRef disagreement outranks a matching compatibility path",
);
const sameSessionPlan = activeTabHydrationPlan(
  { sessionPath: "a.jsonl", sessionGeneration: 3, sessionRevision: 8, sessionDigest: "rev-8" },
  { sessionPath: "a.jsonl", sessionGeneration: 3 },
  false,
);
ok(sameSessionPlan.surfacePolicy === "preserve-current", "backend sync preserves a proven same-session surface");
ok(sameSessionPlan.loadOptions.preserveCachedHistory, "same-session backend sync may reuse its resident history");
const reboundPlan = activeTabHydrationPlan(
  { sessionPath: "a.jsonl", sessionGeneration: 4, sessionRevision: 9, sessionDigest: "rev-9" },
  { sessionPath: "a.jsonl", sessionGeneration: 3 },
  false,
);
ok(reboundPlan.surfacePolicy === "replace-surface", "backend sync replaces a generation-rebound surface");
ok(reboundPlan.loadOptions.sessionGeneration === 4, "replace-surface hydration carries the target generation fence");
ok(
  canAdoptUnboundLiveSurface(
    { sessionPath: "a.jsonl", sessionGeneration: 3 },
    undefined,
    { running: true, live: { text: "partial" }, items: [{ kind: "assistant", streaming: true }] },
    true,
  ),
  "an unbound live runtime tail can be adopted before its first metadata snapshot",
);
ok(
  !canAdoptUnboundLiveSurface(
    { sessionPath: "a.jsonl" },
    { sessionPath: "b.jsonl" },
    { running: true, live: { text: "stale" }, items: [{ kind: "assistant", streaming: true }] },
    true,
  ),
  "a differently identified surface cannot be adopted as a live tail",
);
ok(
  !canAdoptUnboundLiveSurface(
    { sessionPath: "a.jsonl" },
    undefined,
    { running: true, historyTotalTurns: 1, hydrateHistoryLoaded: true, items: [{ kind: "assistant", streaming: true }] },
    true,
  ),
  "a surface with persisted history is never adopted without session identity",
);
ok(
  !canAdoptUnboundLiveSurface(
    { sessionPath: "a.jsonl" },
    undefined,
    { running: true, live: { text: "stale epoch" }, items: [{ kind: "assistant", streaming: true }] },
    true,
    "runtime-new",
    "runtime-old",
  ),
  "a mismatched runtime epoch rejects an unbound live tail",
);

ok(
  !shouldPreferResidentHistory(false, false),
  "retry / explicit no-cache hydrates must not serve the resident snapshot",
);
ok(shouldPreferResidentHistory(false, true), "preserveCachedHistory still allows a resident hit");
ok(shouldPreferResidentHistory(false, undefined), "unspecified preserveCachedHistory still allows a resident hit");
ok(!shouldPreferResidentHistory(true, true), "reset hydrates never prefer the resident snapshot");

const liveIdle = {
  items: [{ kind: "user" }, { kind: "assistant" }, { kind: "user" }],
  historyRevision: 10,
  historyDigest: "rev-10",
};
ok(
  mode(false, true, false, liveIdle, {
    items: [{ kind: "user" }],
    revision: 10,
    digest: "rev-10",
  }) === "skip",
  "idle transcript is not replaced by a shorter same-fingerprint resident snapshot",
);
ok(
  mode(false, true, false, liveIdle, {
    items: [{ kind: "user" }, { kind: "assistant" }, { kind: "user" }, { kind: "assistant" }],
    revision: 11,
    digest: "rev-11",
  }) === "replace",
  "a newer backend page still replaces the idle transcript",
);
ok(
  mode(false, true, false, { items: [] }, {
    items: [{ kind: "user" }],
    revision: 10,
    digest: "rev-10",
  }) === "replace",
  "empty idle surface still applies history",
);

const coldTarget = { sessionPath: "/fixture/legacy.jsonl", sessionGeneration: 4 } as TabMeta;
const coldState = { meta: { ...coldTarget, eventChannel: "agent:event" } as Meta, historyDigest: "verified-cut", historyRevision: 7 };
ok(coldHistoryRefreshProof(coldTarget, coldState, true)?.digest === "verified-cut", "passive metadata without a digest retains the existing certified cut");
ok(!coldHistoryRefreshProof(coldTarget, coldState, false), "explicit reset cannot reuse passive proof");
ok(!coldHistoryRefreshProof({ ...coldTarget, sessionGeneration: 5 }, coldState, true), "a new storage generation cannot reuse the old cut");
ok(!coldHistoryRefreshProof({ ...coldTarget, sessionDigest: "replacement" }, coldState, true), "a changed content proof requires a fresh read");
ok(!coldHistoryRefreshProof({ ...coldTarget, sessionRevision: 8 }, coldState, true), "a changed content revision requires a fresh read");
ok(!coldHistoryRefreshProof(coldTarget, { ...coldState, historyDigest: "" }, true), "an unproven window cannot be retained");
ok(!coldHistoryRefreshProof(coldTarget, { ...coldState, hydrating: true }, true), "unfinished preparation still joins the reader task");
ok(!coldHistoryRefreshProof(coldTarget, { ...coldState, hydrateError: "damaged" }, true), "failed history is not certified by metadata refresh");

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
