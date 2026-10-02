import assert from "node:assert/strict";
import test from "node:test";
import { TranscriptStore } from "../lib/transcriptStore";
import { initialState, reducer } from "../lib/useController";

test("durable user handoff survives actual store reclaim and reload", () => {
  const store = new TranscriptStore({ HistorySliceForTab: async () => { throw new Error("unexpected slice read"); }, HistoryContentForTab: async () => { throw new Error("unexpected content read"); } }, { windowMaxPages: 1, windowPageEntries: 2 });
  const durable = {
    entryId: "m:first",
    turn: 1,
    order: 0,
    message: { role: "user" as const, content: "question", messageId: "first", submissionId: "submit-first" },
    refs: [],
  };
  const installed = store.installSlice("tab-handoff", "/s/handoff.jsonl", {
    entries: [durable], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false,
    startTurn: 1, endTurn: 1, totalTurns: 1, revision: 1, digest: "handoff-1", stale: false,
  });
  let state = reducer({ ...initialState, transcriptProtocol: 2 }, {
    type: "user", text: "question", seq: 0, submissionId: "submit-first",
  });
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: { ...installed, removeIds: [] } });
  assert.equal(state.localSubmissionOrder.length, 0, "durable install retires the local submission echo");
  assert.equal(state.items.filter(item => item.kind === "user" && item.id === "m:first").length, 1, "durable install uses the canonical message id once");

  const reclaimed = store.appendEntries("tab-handoff", "/s/handoff.jsonl", [
    { entryId: "m:next", turn: 2, order: 1, message: { role: "user", content: "next", messageId: "next" }, refs: [] },
    { entryId: "m:answer", turn: 2, order: 2, message: { role: "assistant", content: "answer", messageId: "answer" }, refs: [] },
  ])!;
  assert.ok(reclaimed.removeIds.includes("m:first"), "the real bounded store reclaims the durable user row");
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: reclaimed });
  assert.equal(state.items.some(item => item.id === "m:first"), false, "store reclaim removes the canonical row without reviving its echo");
  assert.equal(state.localSubmissionOrder.length, 0, "store reclaim cannot restore a retired local echo");

  const reloaded = store.installSlice("tab-handoff", "/s/handoff.jsonl", {
    entries: [durable], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false,
    startTurn: 1, endTurn: 1, totalTurns: 1, revision: 2, digest: "handoff-2", stale: false,
  });
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: { ...reloaded, removeIds: reclaimed.items.map(item => item.id) } });
  assert.equal(state.items.filter(item => item.kind === "user" && item.id === "m:first").length, 1, "reloading the reclaimed page restores exactly one durable user row");
});
