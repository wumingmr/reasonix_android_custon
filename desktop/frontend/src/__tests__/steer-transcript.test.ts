import assert from "node:assert/strict";
import test from "node:test";
import type { FollowRequest, TranscriptFollowResponse, Change } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

Object.defineProperty(globalThis, "window", { configurable: true, value: {} });
const commands: Record<string, unknown> = {};
installDesktopHostStub(commands);
const [{ TranscriptSessionFollower }, { initialState, reducer }, { getTranscriptStore }, { ChatSource }] = await Promise.all([
  import("../lib/transcriptSessionFollower"), import("../lib/useController"), import("../lib/transcriptStore"), import("../lib/chatViewSource"),
]);
const text = "对比源码信息应该更准一些";
const prefix = "[Mid-turn steer queued by the user. Do not treat this as a new task; use it only as additional guidance for the current task after completing the current step.]";
const row = (id: string) => ({ recordId: `m:${id}`, messageId: id, role: "notice", content: `↪ ${text}`, turnId: "turn" });
const initial = (tab: string): TranscriptFollowResponse => ({
  protocolVersion: 2, subscription: tab, changes: [], resetRequired: false,
  snapshot: {
    protocolVersion: 1, snapshotId: "cut", identity: { sessionId: tab, runtimeEpoch: "epoch", rewriteEpoch: 0, headId: "" },
    projectionRevision: 10, coveredThroughSeq: 4, durableSeq: 4, records: [], activeRecords: [], activeAttempts: [],
    runtime: { status: "in_progress", turnId: "turn", pendingEvents: [], samplingCount: 1, toolCount: 0 },
    before: 0, hasOlder: false, totalRecords: 0, totalTurns: 1, stale: false,
  },
  history: { status: "ready", snapshotSequence: 4, coverageSequence: 4, generation: "generation", totalTurns: 1,
    hasOlder: false, hasNewer: false, messages: [] },
});
async function microtasks() { for (let i = 0; i < 60; i++) await Promise.resolve(); }

for (const remote of [false, true]) for (const ordering of ["record-first", "event-first", "legacy-event"] as const) {
  test(`${remote ? "remote" : "local"} ${ordering} steer keeps one row and acknowledges the exact inbox item`, async () => {
    const tab = `steer-${remote}-${ordering}`, path = `/session/${tab}`;
    let deliver!: (value: TranscriptFollowResponse) => void;
    const pending = new Promise<TranscriptFollowResponse>(resolve => { deliver = resolve; });
    let polls = 0;
    const response = initial(tab);
    const wireRow = (id: string) => ordering === "legacy-event"
      ? { ...row(id), messageId: undefined, recordId: `m:${id}:notice:0` } : row(id);
    commands[remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab"] = (_tab: string, request: FollowRequest) => request.close
      ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
      : request.subscription ? polls++ === 0 ? pending : new Promise(() => {}) : Promise.resolve(response);
    let state = initialState;
    const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
    const source = new ChatSource(tab);
    try {
      await follower.start();
      const changes: Change[] = [];
      let revision = 10, coverage = 4;
      const record = (id: string) => changes.push({ revision: ++revision, firstSeq: ++coverage, commitSeq: coverage,
        durableSeq: coverage, index: 0, records: [wireRow(id)] });
      const event = (id: string) => changes.push({ revision: ++revision, commitSeq: coverage, durableSeq: coverage, index: 0,
        event: { kind: "steer", text, messageId: ordering === "legacy-event" ? undefined : id, itemId: `inbox-${id}` } });
      // Equal text belongs to two distinct user actions; each receipt is also
      // redelivered to verify identity-based idempotency rather than text dedup.
      for (const id of ["first", "second"]) {
        if (ordering === "event-first") { event(id); record(id); } else { record(id); event(id); }
        event(id);
      }
      deliver({ protocolVersion: 2, subscription: tab, resetRequired: false, changes });
      await microtasks();
      assert.deepEqual(state.items.map(item => item.id), ["he:m:first", "he:m:second"]);
      assert.deepEqual(state.guidanceConsumed, { key: "inbox-second", itemId: "inbox-second", text });
      source.update({ items: state.items, running: state.running, hydrating: false, hasOlder: false, loadingOlder: false });
      assert.deepEqual(source.getOrderSnapshot().filter(id => id.startsWith("he:")), ["he:m:first", "he:m:second"]);
      const projection = getTranscriptStore().peek(tab, path)!;
      state = reducer(state, { type: "transcript_records", projection: { ...projection, removeIds: [] }, confirmedUsers: [] });
      assert.equal(state.items.length, 2, "subsequent refresh must not retain an extra live row");
      assert.equal(state.guidanceConsumed?.itemId, "inbox-second", "refresh preserves the independent consumption receipt");

      // Reconnect joins canonical history with the runtime snapshot. Both must
      // address the same message even though the provider role is user.
      follower.stop();
      response.snapshot!.projectionRevision = revision;
      response.snapshot!.coveredThroughSeq = coverage;
      response.snapshot!.durableSeq = coverage;
      response.snapshot!.totalRecords = 2;
      response.snapshot!.records = ["first", "second"].map((id, order) => ({ id: wireRow(id).recordId, order, message: wireRow(id), refs: [] }));
      response.history!.messages = ["first", "second"].map((id, position) => ({ messageId: id, position, version: 1,
        role: "user", eventSequence: coverage, visibleTurn: 1,
        inline: { id, role: "user", content: `${prefix}\n\n${text}`, raw_content: text, origin: "user" } }));
      await follower.start();
      assert.deepEqual(state.items.map(item => item.id), ["he:m:first", "he:m:second"]);
    } finally { source.dispose(); follower.stop(); getTranscriptStore().evictTab(tab); }
  });
}

test("legacy standalone events render while v2 uncorrelated events only acknowledge", () => {
  for (const protocol of [undefined, 2] as const) {
    const state = reducer({ ...initialState, transcriptProtocol: protocol }, { type: "event", e: { kind: "steer", text, itemId: "queued" } });
    assert.equal(state.items.length, protocol === 2 ? 0 : 1);
    assert.equal(state.guidanceConsumed?.itemId, "queued");
    assert.equal(reducer(state, { type: "reset" }).guidanceConsumed, undefined);
  }
});

test("legacy native pages share steer identity while content refs retain their original owner", async () => {
  const { nativeSnapshotWindow } = await import("../lib/nativeTranscriptHistory");
  const snapshot = initial("native-legacy").snapshot!;
  snapshot.totalRecords = 1;
  const recordId = "m:old-message:notice:0";
  snapshot.records = [{ id: recordId, order: 0, message: { role: "notice", content: `↪ ${text}` },
    refs: [{ snapshotId: "cut", recordId, path: ["content"], bytes: 100 }] }];
  const entry = nativeSnapshotWindow(snapshot as unknown as import("../lib/transcriptProtocol").TranscriptSnapshot).entries[0];
  assert.equal(entry.entryId, "m:old-message");
  assert.equal(entry.message.messageId, "old-message");
  assert.equal(entry.refs[0].transcriptRef?.recordId, recordId, "normalizing display must not invalidate the server's content ref");
});

test("unapplied queue warnings join canonical records in either order", async () => {
  const [{ historyMessagesToItems }, { canonicalMessage }, { nativeSnapshotWindow }] = await Promise.all([
    import("../lib/historyItems"), import("../lib/canonicalTranscriptBackend"), import("../lib/nativeTranscriptHistory"),
  ]);
  const warning = "Guidance was not applied because the turn ended before it could be processed. Send it again if it is still needed:\nSame guidance";
  for (const ordering of ["event-first", "record-first"] as const) {
    let state: import("../lib/useController").State = { ...initialState, transcriptProtocol: 2 };
    const formal: import("../lib/useController").Item[] = [];
    for (const id of ["first", "second"]) {
      const event = { type: "event" as const, e: { kind: "notice" as const, code: "unapplied_steer", level: "warn" as const,
        messageId: id, itemId: `inbox-${id}`, text: warning } };
      if (ordering === "event-first") state = reducer(state, event);
      formal.push(...historyMessagesToItems([{ role: "notice", code: "unapplied_steer", level: "warn", content: warning,
        messageId: id, recordId: `m:${id}:notice:0` }], "snapshot:").items);
      state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: {
        items: [...formal], removeIds: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false, hasNewer: false,
        revision: formal.length, revisionKnown: true, digest: "unapplied",
      } });
      state = reducer(state, event);
      state = reducer(state, event);
    }
    assert.deepEqual(state.items.map(item => item.id), ["he:m:first", "he:m:second"], ordering);
  }

  const oldRecordId = "m:old-warning:notice:0";
  const legacy = { role: "notice", code: "unapplied_steer", level: "warn" as const, content: warning, recordId: oldRecordId };
  assert.equal(historyMessagesToItems([legacy], "snapshot:").items[0]?.id, "he:m:old-warning");
  const snapshot = initial("old-warning").snapshot!;
  snapshot.totalRecords = 1;
  snapshot.records = [{ id: oldRecordId, order: 0, message: legacy, refs: [] }];
  assert.equal(nativeSnapshotWindow(snapshot as unknown as import("../lib/transcriptProtocol").TranscriptSnapshot).entries[0]?.entryId, "m:old-warning");

  const persisted = canonicalMessage({ messageId: "queued", role: "tool", preview: "" } as import("../generated/desktopContract.generated").PersistentMessage,
    { id: "queued", role: "tool", local_only: true, content: `${prefix}\nSame guidance` });
  assert.deepEqual({ role: persisted.role, messageId: persisted.messageId, code: persisted.code, content: persisted.content },
    { role: "notice", messageId: "queued", code: "unapplied_steer", content: "\nSame guidance" });
  const multiline = canonicalMessage({ messageId: "multiline", role: "tool", preview: "" } as import("../generated/desktopContract.generated").PersistentMessage,
    { id: "multiline", role: "tool", local_only: true, content: `${prefix}\nFirst line\nSecond line` });
  const multilineItem = historyMessagesToItems([multiline], "canonical:").items[0];
  if (multilineItem?.kind !== "notice") throw new Error("multiline guidance must render as a notice");
  assert.ok(multilineItem.text.endsWith("First line\nSecond line"));
});

for (const remote of [false, true]) for (const ordering of ["record-first", "event-first"] as const) {
  test(`${remote ? "remote" : "local"} ${ordering} unapplied warning survives follower reconnect as one row`, async () => {
    const tab = `unapplied-${remote}-${ordering}`, path = `/session/${tab}`, id = "queued";
    const warning = "Guidance was not applied because the turn ended before it could be processed. Send it again if it is still needed:\nSame guidance";
    const wireRow = { role: "notice", messageId: id, recordId: `m:${id}:notice:0`, code: "unapplied_steer", level: "warn", content: warning };
    let deliver!: (value: TranscriptFollowResponse) => void;
    const pending = new Promise<TranscriptFollowResponse>(resolve => { deliver = resolve; });
    let polls = 0;
    const response = initial(tab);
    commands[remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab"] = (_tab: string, request: FollowRequest) => request.close
      ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
      : request.subscription ? polls++ === 0 ? pending : new Promise(() => {}) : Promise.resolve(response);
    let state = initialState;
    const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
    try {
      await follower.start();
      const record: Change = { revision: 11, firstSeq: 5, commitSeq: 5, durableSeq: 5, index: 0, records: [wireRow] };
      const event: Change = { revision: 12, commitSeq: ordering === "event-first" ? 4 : 5, durableSeq: 5, index: 0,
        event: { kind: "notice", code: "unapplied_steer", level: "warn", messageId: id, itemId: "inbox-queued", text: warning } };
      deliver({ protocolVersion: 2, subscription: tab, resetRequired: false,
        changes: ordering === "record-first" ? [record, event] : [{ ...event, revision: 11 }, { ...record, revision: 12 }] });
      await microtasks();
      assert.deepEqual(state.items.map(item => item.id), ["he:m:queued"]);
      follower.stop();
      response.snapshot!.projectionRevision = 12;
      response.snapshot!.coveredThroughSeq = 5;
      response.snapshot!.durableSeq = 5;
      response.snapshot!.totalRecords = 1;
      response.snapshot!.records = [{ id: wireRow.recordId, order: 0, message: wireRow, refs: [] }];
      response.history!.messages = [{ messageId: id, position: 0, version: 1, role: "tool", eventSequence: 5, visibleTurn: 1,
        inline: { id, role: "tool", content: `${prefix}\nSame guidance`, local_only: true,
          tool_call_id: "__reasonix_local_only__", name: "__reasonix_local_only__" } }];
      await follower.start();
      assert.deepEqual(state.items.map(item => item.id), ["he:m:queued"]);
    } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
  });
}
