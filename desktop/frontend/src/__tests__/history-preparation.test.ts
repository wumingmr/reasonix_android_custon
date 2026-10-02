import assert from "node:assert/strict";
import { HistoryPreparingError, loadPreparedHistory } from "../lib/historyPreparation";
import { TranscriptStore } from "../lib/transcriptStore";
import type { HistorySlice } from "../lib/types";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { canonicalHistorySlice } from "../lib/canonicalTranscriptBackend";

let calls = 0;
let waits = 0;
const page = await loadPreparedHistory(async () => {
  if (++calls < 3) throw new HistoryPreparingError();
  return "ready";
}, () => true, async () => { waits++; });
assert.equal(page, "ready");
assert.equal(waits, 2);

let current = true;
let cancelledCalls = 0;
const cancelled = await loadPreparedHistory(async () => {
  cancelledCalls++;
  throw new HistoryPreparingError();
}, () => current, async () => { current = false; });
assert.equal(cancelled, undefined);
assert.equal(cancelledCalls, 1, "session changes stop preparation requests");

const corruption = new Error("damaged session store");
await assert.rejects(loadPreparedHistory(async () => { throw corruption; }, () => true,
  async () => { assert.fail("real errors must not be retried"); }), error => error === corruption);

let finish!: (value: string) => void;
current = true;
const pending = loadPreparedHistory(() => new Promise<string>(resolve => { finish = resolve; }), () => current);
current = false;
finish("old session");
assert.equal(await pending, undefined, "late ready response cannot hydrate another session");

const readySlice: HistorySlice = { entries: [{ entryId: "m1", turn: 1, order: 1, message: { role: "user", content: "ready history" }, refs: [] }], nextCursor: "", hasOlder: false, totalTurns: 1, startTurn: 1, endTurn: 1, stale: false, revision: 1 };
let preparationCalls = 0;
const store = new TranscriptStore({
  HistorySliceForTab: async () => { if (++preparationCalls === 1) throw new HistoryPreparingError(); return readySlice; },
  HistoryContentForTab: async () => { throw new Error("unused"); },
}, { preparationWait: async () => {} });
const loaded = await store.loadLatest("tab", "session");
assert.equal(loaded?.totalTurns, 1, "store completes startup hydration after preparation");
assert.equal(preparationCalls, 2);

let resume!: () => void;
let waitStarted!: () => void;
const started = new Promise<void>(resolve => { waitStarted = resolve; });
let staleCalls = 0;
const changingStore = new TranscriptStore({
  HistorySliceForTab: async () => { if (++staleCalls === 1) throw new HistoryPreparingError(); return readySlice; },
  HistoryContentForTab: async () => { throw new Error("unused"); },
}, { preparationWait: () => new Promise<void>(resolve => { resume = resolve; waitStarted(); }) });
const oldLoad = changingStore.loadLatest("tab", "session");
await started;
const newLoad = await changingStore.loadLatest("tab", "session");
resume();
assert.equal(await oldLoad, undefined, "superseded store generation cannot restart its request");
assert.equal(newLoad?.totalTurns, 1);
assert.equal(staleCalls, 2, "superseded request performs no extra RPC");

let pagingCalls = 0;
current = true;
const pagingStore = new TranscriptStore({
  HistorySliceForTab: async (_tab, req) => {
    assert.equal("current" in req, false, "lifecycle callback cannot enter the IPC payload");
    if (++pagingCalls > 1) throw new HistoryPreparingError();
    return { ...readySlice, hasOlder: true, nextCursor: "older" };
  },
  HistoryContentForTab: async () => { throw new Error("unused"); },
}, { preparationWait: async () => { current = false; } });
await pagingStore.loadLatest("tab", "session");
assert.equal(await pagingStore.loadOlder("tab", "session", { current: () => current }), undefined);
assert.equal(pagingCalls, 2, "navigation cancels preparation of an older page");

const dom = new JSDOM("", { url: "http://localhost/" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;
let remoteReads = 0;
const host = installDesktopHostStub({
  SessionHistoryWindowForTab: async () => ({ status: "preparing", messages: [] }),
  RemoteSessionHistoryWindowForTab: async () => { remoteReads++; throw new Error("wrong host"); },
});
await assert.rejects(canonicalHistorySlice("preparing-local", { cursor: "" }), HistoryPreparingError);
assert.equal(remoteReads, 0, "preparing local history must not fall back to remote");
host.uninstall();
dom.window.close();
console.log("history preparation: ready, cancellation, late completion and real errors passed");
