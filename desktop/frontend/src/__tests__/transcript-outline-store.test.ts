import assert from "node:assert/strict";
import { TranscriptOutlineStore, type OutlineRead } from "../lib/transcriptOutlineStore";
import type { HistoryOutlinePage, HistoryOutlineRequest } from "../generated/desktopContract.generated";

const requests: HistoryOutlineRequest[] = [];
const page = (start = 1): HistoryOutlinePage => ({ status: "ready", generation: "cut", snapshotSequence: 12,
  coverageSequence: 12, totalTurns: 25000, nextTurn: start + 128, done: false,
  entries: Array.from({ length: 128 }, (_, i) => ({ messageId: `u${start + i}`, turn: start + i, position: start + i, prompt: `question ${start + i}` })) });
const read: OutlineRead = async (_tab, req) => { requests.push(req); return page(req.startTurn); };
const store = new TranscriptOutlineStore();
const off = store.activate("tab", "session", read, 25000);
await store.refresh("tab");
assert.equal(store.getView("tab").totalTurns, 25000);
assert.equal(requests.length, 1, "initial read is one page, not the whole history");
await Promise.all([store.ensure("tab", 20001), store.ensure("tab", 20001)]);
assert.equal(requests.length, 2, "range requests deduplicate");
assert.equal(requests[1].generation, "cut");
assert.equal(requests[1].snapshotSequence, 12);
for (let turn = 1000; turn < 10000; turn += 1000) await store.ensure("tab", turn);
assert.ok(store.getView("tab").entries.size <= 6 * 128, "summary cache is bounded");
assert.equal(store.getView("tab").totalTurns, 25000, "eviction preserves the rail extent");
await store.ensure("tab", 1);
assert.equal(store.getView("tab").entries.get(1)?.messageId, "u1");
off();

let resolve!: (page: HistoryOutlinePage) => void;
const stale = new Promise<HistoryOutlinePage>(done => { resolve = done; });
const oldOff = store.activate("same-tab", "old-session", () => stale, 2);
const pending = store.refresh("same-tab");
await Promise.resolve();
const newOff = store.activate("same-tab", "new-session", async () => ({ ...page(), totalTurns: 1, entries: [] }), 1);
await store.refresh("same-tab");
resolve(page()); await pending; oldOff();
assert.equal(store.getView("same-tab").totalTurns, 1, "old response and cleanup cannot replace the new session");
newOff();

const failing = store.activate("failed", "session", async () => { throw new Error("network"); }, 5);
await store.refresh("failed");
assert.equal(store.getView("failed").mode, "error");
assert.equal(store.getView("failed").totalTurns, 5);
failing();
const unsupported = store.activate("old-host", "session", async () => ({ ...page(), status: "unsupported", entries: [] }), 5);
await store.refresh("old-host");
assert.equal(store.getView("old-host").mode, "unsupported"); unsupported();
console.log("durable outline: sparse pages, fixed cut, eviction, generation and capability passed");

{
  let fail = true;
  const reads: number[] = [];
  const cache = new TranscriptOutlineStore();
  const release = cache.activate("retry-page", "session", async (_tab, req) => {
    reads.push(req.startTurn ?? 1);
    if (req.startTurn === 129 && fail) throw new Error("page offline");
    return page(req.startTurn);
  }, 25000);
  try {
    await cache.refresh("retry-page");
    await cache.ensure("retry-page", 129);
    await cache.ensure("retry-page", 257);
    assert.equal(cache.getView("retry-page").mode, "error", "another successful page cannot hide a failed page");
    fail = false;
    await cache.retry("retry-page");
    assert.equal(cache.getView("retry-page").entries.get(129)?.messageId, "u129", "retry reloads the failed range even when the cut is unchanged");
    assert.equal(cache.getView("retry-page").mode, "ready");
    assert.equal(reads.filter(start => start === 129).length, 2);
  } finally { release(); }
}
{
  let cut = 12;
  let finish!: (value: HistoryOutlinePage) => void;
  const cache = new TranscriptOutlineStore();
  const release = cache.activate("cut-race", "session", async (_tab, req) => {
    if (req.startTurn === 129 && req.snapshotSequence === 12) return new Promise(resolve => { finish = resolve; });
    return { ...page(req.startTurn), snapshotSequence: cut };
  }, 25000);
  try {
    await cache.refresh("cut-race");
    const old = cache.ensure("cut-race", 129);
    await Promise.resolve(); await Promise.resolve();
    cut = 13;
    await cache.refresh("cut-race");
    finish(page(129)); await old;
    await cache.ensure("cut-race", 129);
    assert.equal(cache.getView("cut-race").snapshotSequence, 13);
    assert.equal(cache.getView("cut-race").mode, "ready", "an obsolete page cannot poison the fresh directory");
    assert.equal(cache.getView("cut-race").entries.get(129)?.messageId, "u129");
  } finally { release(); }
}
{
  let finish!: (value: HistoryOutlinePage) => void;
  let reads = 0;
  const gate = new Promise<HistoryOutlinePage>(resolve => { finish = resolve; });
  const cache = new TranscriptOutlineStore();
  const release = cache.activate("slow", "session", () => { reads++; return gate; }, 25000);
  try {
    const first = cache.refresh("slow");
    await Promise.resolve();
    const second = cache.refresh("slow");
    await Promise.resolve();
    assert.equal(reads, 1, "slow refresh is shared instead of continually invalidated");
    finish(page()); await Promise.all([first, second]);
    assert.equal(cache.getView("slow").mode, "ready");
  } finally { release(); }
}
