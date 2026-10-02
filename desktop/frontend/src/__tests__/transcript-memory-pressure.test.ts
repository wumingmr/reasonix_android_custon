import assert from "node:assert/strict";
import { TranscriptStore } from "../lib/transcriptStore";
import { initialState, reducer } from "../lib/useController";
import { FakeBackend } from "./helpers/transcriptFakeBackend";
import { hasReusableCachedTranscript } from "../lib/hydrateHistoryApply";

const body = "full content ".repeat(1000);
const backend = new FakeBackend([{ role: "user", content: "prompt" }, { role: "assistant", content: "preview" }],
  new Map([["s1:r0:m1:o0:content", body]]));
const store = new TranscriptStore(backend, { historyBodyBudgetBytes: 1024, maxResidentSessions: 1 });
store.setPinned("background", true);
await store.loadLatest("background", "/background");
assert.equal(await store.requestFullContent("background", "s1:r0:m1:o0", "content"), body, "explicit content remains complete");
assert.ok(store.totalBodyBytes() <= 1024, "invisible expanded body is reclaimed even with a live pin");
assert.ok(store.isResident("background", "/background"), "memory pressure cannot retire the running session");
const reads = backend.contentCalls.length;
assert.equal(await store.requestFullContent("background", "s1:r0:m1:o0", "content"), body);
assert.ok(backend.contentCalls.length > reads, "reclaimed content remains reachable");
store.setPinned("background", false);
const state = { ...initialState, historyPrefixCount: 1, historyTotalTurns: 12, historyOlderLoading: true, items: [
  { kind: "user" as const, id: "history", text: "cached" },
  { kind: "user" as const, id: "pending", text: "pending" },
] };
let released = false;
store.subscribe("background", change => {
  if (change.evictedPath === "/background") {
    released = true;
    const next = reducer(state, { type: "history_cache_evicted" });
    assert.deepEqual(next.items.map(item => item.id), ["pending"], "eviction releases cached rows but preserves pending input");
    assert.equal(hasReusableCachedTranscript(next, {}), false, "pending input cannot masquerade as resident history without a fingerprint");
    assert.equal(next.historyOlderLoading, false, "eviction releases superseded paging indicators");
  }
});
await store.loadLatest("other", "/other");
assert.ok(released, "LRU eviction notifies the controller to release its references");
assert.ok(!store.isResident("background", "/background"));

// Canonical bodies can reveal tool rows, so reclamation must also remove their
// structural projection rather than publishing only field patches.
const canonical = JSON.stringify({ role: "assistant", content: "expanded", tool_calls: [
  { id: "revealed-call", name: "bash", arguments: "x".repeat(4000) },
] });
const canonicalBackend = new FakeBackend([], new Map([["owner:canonicalMessage", canonical]]));
const canonicalStore = new TranscriptStore(canonicalBackend, { historyBodyBudgetBytes: 1024 });
canonicalStore.noteActiveTab("canonical");
canonicalStore.setPinned("canonical", true);
canonicalStore.installSlice("canonical", "/canonical", {
  entries: [{ entryId: "owner", turn: 1, order: 0, message: { role: "assistant", content: "preview" },
    refs: [{ entryId: "owner", field: "canonicalMessage", size: canonical.length, chunks: 2, revision: 1, digest: "body" }] }],
  nextCursor: "", hasOlder: false, totalTurns: 1, startTurn: 1, endTurn: 1, stale: false, revision: 1, digest: "body",
});
let removedTool = false;
canonicalStore.subscribe("canonical", change => {
  if (change.projection?.removeIds?.includes("revealed-call")) removedTool = true;
});
await canonicalStore.requestFullContent("canonical", "owner", "content");
canonicalStore.noteActiveTab("other", "canonical");
assert.ok(removedTool, "reclamation publishes removal of tool rows revealed by an expanded canonical body");
assert.ok(canonicalStore.isResident("canonical", "/canonical"), "structural reclamation preserves the live session");
{
  const backend = new FakeBackend([{ role: "user", content: "u" }, { role: "assistant", content: "a" }]);
  const store = new TranscriptStore(backend, { maxResidentSessions: 3 });
  let previous: string | undefined;
  for (let i = 0; i < 8; i++) {
    const tab = `active-${i}`;
    store.noteActiveTab(tab, previous);
    await store.loadLatest(tab, `/s/${i}.jsonl`);
    assert.equal(store.residentSessionCount(), Math.min(i + 1, 3), "continuous navigation includes active window in resident cap");
    assert.equal(store.isResident(tab, `/s/${i}.jsonl`), true, "active reader is protected");
    previous = tab;
  }
  const protectedStore = new TranscriptStore(backend, { maxResidentSessions: 1 });
  for (const tab of ["running-a", "running-b"]) {
    protectedStore.setPinned(tab, true);
    await protectedStore.loadLatest(tab, `/s/${tab}`);
  }
  assert.equal(protectedStore.residentSessionCount(), 2, "budget pressure does not evict live execution owners");
  protectedStore.setPinned("running-a", false);
  assert.equal(protectedStore.residentSessionCount(), 1, "ending a live pin immediately restores the cache budget");
}

console.log("transcript memory pressure: passed");
