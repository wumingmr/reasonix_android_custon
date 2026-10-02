import assert from "node:assert/strict";
import test from "node:test";
import { installDesktopHostStub } from "./desktopHostStub";
import type { TranscriptSnapshot } from "../lib/transcriptProtocol";

Object.defineProperty(globalThis, "window", { configurable: true, value: {} });
const commands: Record<string, unknown> = {};
installDesktopHostStub(commands);
const { bindNativeTranscriptHistory, nativeSnapshotWindow, readNativeTranscriptWindow, readNativeTranscriptOutline } = await import("../lib/nativeTranscriptHistory");

function snapshot(before: number, count: number, total = 100): TranscriptSnapshot {
  const start = Math.max(0, before - count);
  return { protocolVersion: 1, snapshotId: "cut", identity: { sessionId: "old", headId: "main", rewriteEpoch: 0, runtimeEpoch: "epoch" },
    coveredThroughSeq: 7, projectionRevision: 7, runtime: { pendingEvents: [] }, activeRecords: [], activeAttempts: [],
    records: Array.from({ length: before - start }, (_, index) => ({ id: `m:${start + index}`, order: start + index,
      message: { role: "user", messageId: String(start + index), content: "text", historyTurn: start + index + 1 }, refs: [] })),
    before: start, hasOlder: start > 0, totalRecords: total, totalTurns: total, stale: false };
}

test("native pages remain bounded and reachable in both directions", async () => {
  commands.TranscriptPageForTab = async (_: string, req: { before: number; records: number }) => snapshot(Math.min(100, req.before), req.records);
  const initial = snapshot(100, 32);
  const release = bindNativeTranscriptHistory("native", initial, false);
  let page = nativeSnapshotWindow(initial);
  const seen = new Set(page.entries.map(entry => entry.entryId));
  while (page.hasOlder) {
    page = (await readNativeTranscriptWindow("native", { anchor: "cursor", cursor: page.olderCursor, limit: 32 }))!;
    assert.ok(page.entries.length <= 32);
    page.entries.forEach(entry => seen.add(entry.entryId));
  }
  assert.equal(seen.size, 100);
  let nextOrder = page.entries[page.entries.length - 1]!.order + 1;
  while (page.hasNewer) {
    page = (await readNativeTranscriptWindow("native", { anchor: "cursor", cursor: page.newerCursor, limit: 32 }))!;
    assert.ok(page.entries.length <= 32);
    assert.equal(page.entries[0].order, nextOrder, "forward pages neither overlap nor skip");
    nextOrder = page.entries[page.entries.length - 1]!.order + 1;
  }
  assert.equal(page.entries[page.entries.length - 1]?.entryId, "m:99");
  release();
});

test("a requested fixed cut is retained and conflicting cursors are stale", async () => {
  commands.TranscriptPageForTab = async (_: string, req: { snapshotId: string; before: number; records: number }) => {
    assert.equal(req.snapshotId, "previous-cut");
    assert.equal(req.before, undefined, "newest uses the requested cut's end, not the live binding's size");
    return { ...snapshot(150, req.records, 150), snapshotId: req.snapshotId };
  };
  const release = bindNativeTranscriptHistory("fixed", snapshot(100, 32), false);
  const fixed = await readNativeTranscriptWindow("fixed", { generation: "previous-cut", anchor: "newest" });
  assert.equal(fixed?.digest, "previous-cut");
  assert.equal(fixed?.entries[fixed.entries.length - 1]?.order, 149);
  const old = nativeSnapshotWindow(snapshot(100, 32));
  assert.equal((await readNativeTranscriptWindow("fixed", { anchor: "cursor", generation: "previous-cut", cursor: old.olderCursor }))?.status, "stale_cursor");
  assert.equal((await readNativeTranscriptWindow("fixed", { anchor: "cursor", cursor: "broken" }))?.status, "stale_cursor");
  release();
});

test("late native pages cannot publish after replacement and never fall back to another host", async () => {
  let finish!: (value: TranscriptSnapshot) => void;
  commands.TranscriptPageForTab = () => new Promise<TranscriptSnapshot>(resolve => { finish = resolve; });
  commands.RemoteTranscriptPageForTab = () => { throw new Error("wrong host"); };
  const releaseOld = bindNativeTranscriptHistory("switched", snapshot(100, 32), false);
  const pending = readNativeTranscriptWindow("switched", { anchor: "newest", limit: 32 });
  const releaseNew = bindNativeTranscriptHistory("switched", snapshot(100, 32), false);
  releaseOld();
  finish(snapshot(100, 32));
  assert.equal((await pending)?.status, "stale_cursor");
  commands.TranscriptPageForTab = () => { throw new Error("disk error"); };
  await assert.rejects(readNativeTranscriptWindow("switched", { anchor: "newest" }), /disk error/);
  releaseNew();
  assert.equal(await readNativeTranscriptWindow("switched", { anchor: "newest" }), undefined);
});

test("a byte-limited forward page does not skip oversized records", async () => {
  commands.TranscriptPageForTab = async (_: string, req: { before: number }) => snapshot(req.before, 1);
  const release = bindNativeTranscriptHistory("large", snapshot(100, 32), false);
  const older = nativeSnapshotWindow(snapshot(32, 32));
  const next = await readNativeTranscriptWindow("large", { anchor: "cursor", cursor: older.newerCursor, limit: 32 });
  assert.equal(next?.entries[0].order, 32);
  release();
});

test("late missing-turn lookup cannot publish into a replacement binding", async () => {
  let finish!: (value: unknown) => void;
  commands.TranscriptOutlineForTab = () => new Promise(resolve => { finish = resolve; });
  commands.TranscriptPageForTab = () => { throw new Error("retired lookup continued"); };
  const releaseOld = bindNativeTranscriptHistory("turn-switch", snapshot(100, 32), false);
  const pending = readNativeTranscriptWindow("turn-switch", { anchor: "turn", turn: 1000 });
  const releaseNew = bindNativeTranscriptHistory("turn-switch", snapshot(100, 32), false);
  releaseOld();
  finish({ entries: [], stale: false });
  assert.equal((await pending)?.status, "stale_cursor");
  releaseNew();
});

test("remote native message and outline lookup use the negotiated host and fixed cut", async () => {
  commands.TranscriptPageForTab = () => { throw new Error("wrong host"); };
  commands.TranscriptOutlineForTab = () => { throw new Error("wrong host"); };
  commands.RemoteTranscriptPageForTab = async (_: string, req: { snapshotId: string; messageId: string }) => {
    assert.equal(req.snapshotId, "cut");
    assert.equal(req.messageId, "50");
    return snapshot(51, 1);
  };
  commands.RemoteTranscriptOutlineForTab = async (_: string, req: { snapshotId: string; offset: number }) => {
    assert.equal(req.snapshotId, "cut");
    assert.equal(req.offset, 50);
    return { entries: [{ id: "m:50", messageId: "50", turn: 51, order: 50, prompt: "question", answer: "answer" }], total: 100, nextOffset: 51, done: false, stale: false };
  };
  const release = bindNativeTranscriptHistory("remote-native", snapshot(100, 32), true);
  const message = await readNativeTranscriptWindow("remote-native", { anchor: "message", messageId: "50", generation: "cut" });
  assert.equal(message?.entries[0].entryId, "m:50");
  const outline = await readNativeTranscriptOutline("remote-native", { generation: "cut", startTurn: 51, limit: 1 });
  assert.equal(outline?.entries[0].messageId, "50");
  assert.equal(outline?.nextTurn, 52);
  release();
});
