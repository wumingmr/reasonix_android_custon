import assert from "node:assert/strict";
import test from "node:test";
import type { FollowRequest, TranscriptFollowResponse } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

Object.defineProperty(globalThis, "window", { configurable: true, value: {} });
const commands: Record<string, unknown> = {};
const desktopStub = installDesktopHostStub(commands);
const [{ TranscriptSessionFollower }, { initialState, reducer }, { getTranscriptStore }] = await Promise.all([
  import("../lib/transcriptSessionFollower"), import("../lib/useController"), import("../lib/transcriptStore"),
]);
const { ChatSource } = await import("../lib/chatViewSource");
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

test("expired settled content requests the owning follower to resynchronize", async () => {
  const { canonicalHistoryContent, registerTranscriptContentRecovery } = await import("../lib/canonicalTranscriptBackend");
  let recoveries = 0;
  commands.TranscriptContentForTab = async () => ({ stale: true });
  const release = registerTranscriptContentRecovery("expired-content", () => { recoveries++; });
  const ref = { entryId: "m:answer", field: "content", size: 1, chunks: 1, revision: 1, digest: "expired",
    transcriptRef: { snapshotId: "expired", recordId: "m:answer", path: ["content"], bytes: 1 } };
  await assert.rejects(canonicalHistoryContent("expired-content", ref, 0), /synchronizing/);
  assert.equal(recoveries, 1);
  release();
  await assert.rejects(canonicalHistoryContent("expired-content", ref, 0), /synchronizing/);
  assert.equal(recoveries, 1, "released owner cannot be restarted by a late content result");
  const delayed = deferred<{ stale: boolean }>();
  commands.TranscriptContentForTab = () => delayed.promise;
  const oldRelease = registerTranscriptContentRecovery("expired-content", () => { recoveries++; });
  const staleRead = canonicalHistoryContent("expired-content", ref, 0);
  oldRelease();
  const newRelease = registerTranscriptContentRecovery("expired-content", () => { recoveries += 100; });
  delayed.resolve({ stale: true });
  await assert.rejects(staleRead, /synchronizing/);
  assert.equal(recoveries, 1, "late stale reference cannot resynchronize a replacement session");
  newRelease();
});

test("reading old pages isolates live output and rejoins the current stable node", () => {
  let state: import("../lib/useController").State = { ...initialState, transcriptProtocol: 2, historyHasNewer: true,
    items: [{ kind: "user" as const, id: "old-reader", text: "old page" }] };
  const resident = state.items;
  state = reducer(state, { type: "event", e: { kind: "text", messageId: "active", text: "full prefix" } });
  assert.equal(state.items, resident);
  assert.equal(state.offscreenItems?.find(item => item.id === "m:active")?.kind, "assistant");
  const active = state.offscreenItems!.find(item => item.id === "m:active")!;
  state = reducer(state, { type: "history_append", items: [], startTurn: 0, endTurn: 1, totalTurns: 1, hasOlder: true, hasNewer: false });
  assert.equal(state.items.find(item => item.id === "m:active"), active);
  assert.equal(state.offscreenItems, undefined);
});

test("business replacement preserves authoritative final turn annotations", () => {
  const final = { kind: "assistant" as const, id: "m:final", text: "answer", reasoning: "", streaming: false, turnFinal: true, turnDurationMs: 933524, samplingCount: 72, toolCount: 72 };
  const state = reducer({ ...initialState, items: [final] }, { type: "transcript_records", confirmedUsers: [], projection: {
    items: [{ ...final, turnFinal: undefined, turnDurationMs: undefined, samplingCount: undefined, toolCount: undefined }],
    removeIds: [], startTurn: 0, endTurn: 1, totalTurns: 1, hasOlder: false, hasNewer: false, revision: 2, revisionKnown: true, digest: "cut",
  } });
  assert.equal(state.items[0].kind === "assistant" && state.items[0].turnDurationMs, 933524);
});

test("business projection removes stale duplicate nodes and restores authoritative order", () => {
  const user = { kind: "user" as const, id: "m:user", text: "build" };
  const tool = { kind: "tool" as const, id: "call", name: "edit_file", args: "{}", readOnly: false, status: "done" as const, output: "written" };
  const final = { kind: "assistant" as const, id: "m:final", text: "done", reasoning: "", streaming: false };
  const state = reducer({ ...initialState, items: [user, final, tool, { ...tool }] }, { type: "transcript_records", confirmedUsers: [], projection: {
    items: [user, tool, final], removeIds: [], startTurn: 1, endTurn: 1, totalTurns: 1, hasOlder: false, hasNewer: false,
    revision: 2, revisionKnown: true, digest: "cut",
  } });
  assert.deepEqual(state.items.map(item => item.id), ["m:user", "call", "m:final"]);
});

test("content-driven projection preserves a completed event result", () => {
  const live = { kind: "tool" as const, id: "call", name: "write_file", args: "{}", readOnly: false,
    status: "done" as const, output: "written", execution: { state: "completed" as const, durationMs: 99 } };
  const projected = { ...live, status: "unknown" as const, output: "", execution: undefined,
    resultMissing: true, resultEvidence: "missing" as const };
  const state = reducer({ ...initialState, items: [live], transcriptProjectedIds: ["call"] }, {
    type: "transcript_records", confirmedUsers: [], projection: {
      items: [projected], removeIds: [], mutation: "patch", startTurn: 1, endTurn: 1, totalTurns: 1,
      hasOlder: false, hasNewer: false, revision: 2, revisionKnown: true, digest: "cut",
    },
  });
  const tool = state.items.find((item): item is typeof live => item.kind === "tool");
  assert.equal(tool?.status, "done");
  assert.equal(tool?.output, "written");
  assert.equal(tool?.execution?.durationMs, 99);
});

test("formal empty result replaces an earlier event preview", () => {
  const live = { kind: "tool" as const, id: "call", name: "write_file", args: "{}", readOnly: false,
    status: "done" as const, output: "preview", error: "temporary", execution: { state: "completed" as const } };
  const formal = { ...live, output: "", error: undefined, resultEvidence: "formal" as const };
  const state = reducer({ ...initialState, items: [live], transcriptProjectedIds: ["call"] }, {
    type: "transcript_records", confirmedUsers: [], projection: {
      items: [formal], removeIds: [], mutation: "patch", startTurn: 1, endTurn: 1, totalTurns: 1,
      hasOlder: false, hasNewer: false, revision: 2, revisionKnown: true, digest: "cut",
    },
  });
  const tool = state.items[0];
  assert.equal(tool.kind === "tool" && tool.output, "");
  assert.equal(tool.kind === "tool" && tool.error, undefined);
});

test("duplicate authoritative projection keys are rejected without changing the mounted state", () => {
  const tool = { kind: "tool" as const, id: "call", name: "write_file", args: "{}", readOnly: false,
    status: "done" as const, output: "written" };
  const before = { ...initialState, items: [tool], transcriptProjectedIds: [tool.id] };
  const state = reducer(before, { type: "transcript_records", confirmedUsers: [], projection: {
    items: [tool, { ...tool, output: "conflicting" }], removeIds: [], mutation: "patch",
    startTurn: 1, endTurn: 1, totalTurns: 1, hasOlder: false, hasNewer: false,
    revision: 2, revisionKnown: true, digest: "cut",
  } });
  assert.equal(state, before);
  assert.equal(state.items[0].kind === "tool" && state.items[0].output, "written");
});

test("an id-only late event cannot overwrite an ambiguous formal result", () => {
  const first = { kind: "tool" as const, id: "call", name: "bash", args: "", readOnly: false,
    status: "done" as const, output: "first", identityConflict: true };
  const second = { ...first, id: "call:conflict:second", output: "second" };
  const before = { ...initialState, items: [first, second], transcriptProjectedIds: [first.id, second.id] };
  const state = reducer(before, {
    type: "event", e: { kind: "tool_result", tool: { id: "call", name: "bash", output: "late", readOnly: false } },
  });
  assert.equal(state, before);
  assert.deepEqual(state.items.map(item => item.kind === "tool" && item.output), ["first", "second"]);
});

test("reapplying an identical authoritative projection is a state no-op", () => {
  const tool = { kind: "tool" as const, id: "call", name: "write_file", args: "{}", readOnly: false,
    status: "done" as const, output: "written", resultEvidence: "formal" as const };
  const projection = { items: [tool], removeIds: [], mutation: "patch" as const,
    startTurn: 1, endTurn: 1, totalTurns: 1, hasOlder: false, hasNewer: false,
    revision: 2, revisionKnown: true, digest: "cut" };
  const first = reducer(initialState, { type: "transcript_records", confirmedUsers: [], projection });
  const second = reducer(first, { type: "transcript_records", confirmedUsers: [], projection });
  assert.equal(second, first);
  assert.equal(second.historyMutation.seq, first.historyMutation.seq);
});

test("unrelated lazy body hydration cannot downgrade a completed tool event", async () => {
  const tab = "completed-tool-hydration", path = "/session/completed-tool-hydration";
  const body = "loaded old text";
  commands.HistoryContentForTab = async (_tab: string, ref: { entryId: string; field: string }) => ({
    entryId: ref.entryId, field: ref.field, chunk: 0, chunks: 1, data: body, done: true, stale: false,
  });
  const store = getTranscriptStore();
  const projection = store.installSlice(tab, path, {
    entries: [
      { entryId: "m:old", turn: 1, order: 0, message: { role: "assistant", messageId: "old", content: "preview" },
        refs: [{ entryId: "m:old", field: "content", size: body.length, chunks: 1, revision: 1, digest: "body" }] },
      { entryId: "m:call", turn: 1, order: 1, message: { role: "assistant", messageId: "call", content: "",
        toolCalls: [{ id: "write", name: "write_file", arguments: "{}" }] }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "cut", stale: false,
  });
  let state = reducer({ ...initialState, transcriptProtocol: 2 }, { type: "transcript_records",
    projection: { ...projection, removeIds: [], mutation: "replace" }, confirmedUsers: [] });
  state = reducer(state, { type: "event", remote: true, e: { kind: "tool_result", tool: {
    id: "write", name: "write_file", output: "written", readOnly: false,
    execution: { state: "completed", durationMs: 99 },
  } } });
  const unsubscribe = store.subscribe(tab, change => {
    if (change.projection) state = reducer(state, { type: "transcript_records", projection: change.projection, confirmedUsers: [] });
  });
  try {
    await store.requestFullContent(tab, "m:old", "content");
    const tool = state.items.find(item => item.kind === "tool");
    assert.equal(tool?.kind === "tool" && tool.status, "done");
    assert.equal(tool?.kind === "tool" && tool.output, "written");
    assert.equal(tool?.kind === "tool" && tool.execution?.durationMs, 99);
  } finally { unsubscribe(); store.evictTab(tab); }
});

test("authoritative projection keeps local notices at their persisted anchors", () => {
  const user = { kind: "user" as const, id: "m:user", text: "build" };
  const notice = { kind: "notice" as const, id: "local:notice", local: true, level: "info" as const, text: "checking" };
  const final = { kind: "assistant" as const, id: "m:final", text: "done", reasoning: "", streaming: false };
  const state = reducer({ ...initialState, items: [user, notice, final], transcriptProjectedIds: [user.id, final.id] }, {
    type: "transcript_records", confirmedUsers: [], projection: {
      items: [user, final], removeIds: [], mutation: "patch", startTurn: 1, endTurn: 1, totalTurns: 1,
      hasOlder: false, hasNewer: false, revision: 2, revisionKnown: true, digest: "cut",
    },
  });
  assert.deepEqual(state.items.map(item => item.id), ["m:user", "local:notice", "m:final"]);
});

test("durable user handoff keeps live rows after the user without remounting them", () => {
  const previous = { kind: "assistant" as const, id: "m:previous", text: "before", reasoning: "", streaming: false };
  const process = { kind: "tool" as const, id: "tool:live", name: "shell", args: "{}", readOnly: true,
    status: "running" as const, turnId: "turn-live" };
  const answer = { kind: "assistant" as const, id: "m:answer-live", text: "done", reasoning: "", streaming: false, turnId: "turn-live" };
  const durableUser = { kind: "user" as const, id: "m:user-live", messageId: "user-live", text: "build", turnId: "turn-live" };
  const state = reducer({ ...initialState, items: [previous, process, answer], transcriptProjectedIds: [previous.id],
    localSubmissions: { submit: { submissionId: "submit", localId: "u1", text: "build", createdAt: 1, sequence: 1,
      status: "accepted" as const, messageId: "user-live", turnId: "turn-live" } }, localSubmissionOrder: ["submit"] }, {
    type: "transcript_records", confirmedUsers: [], projection: {
      items: [previous, durableUser], removeIds: [], mutation: "patch", startTurn: 1, endTurn: 2, totalTurns: 2,
      hasOlder: false, hasNewer: false, revision: 2, revisionKnown: true, digest: "cut",
    },
  });
  assert.deepEqual(state.items.map(item => item.id), ["m:previous", "m:user-live", "tool:live", "m:answer-live"]);
  assert.equal(state.items[2], process);
  assert.equal(state.items[3], answer);
});
async function microtasks() { for (let i = 0; i < 16; i++) await Promise.resolve(); }

for (const remote of [false, true]) for (const scenario of ["direct", "event-first", "late", "stale", "missing"] as const) {
  const lateBinding = scenario !== "direct" && scenario !== "event-first";
  test(`${remote ? "remote" : "local"} offscreen submission confirmation (${scenario})`, async () => {
    const tab = `offscreen-${remote}-${scenario}`, path = `/session/${tab}`;
    let state = { ...initialState };
    const polls: Array<ReturnType<typeof deferred<TranscriptFollowResponse>>> = [];
    const readKey = remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab";
    commands[readKey] = (_tab: string, request: FollowRequest) => {
      if (request.close) return Promise.resolve({ protocolVersion: 2, changes: [], resetRequired: false, subscription: tab });
      if (!request.subscription) return Promise.resolve(initial(tab));
      const poll = deferred<TranscriptFollowResponse>(); polls.push(poll); return poll.promise;
    };
    let lookups = 0;
    const lookup = deferred<{ status: string; messages: Array<{ role: string; messageId: string }> }>();
    commands[remote ? "RemoteSessionHistoryWindowForTab" : "SessionHistoryWindowForTab"] = (_tab: string, request: { anchor: string; messageId: string }) => {
      lookups++; assert.equal(request.anchor, "message"); assert.equal(request.messageId, "sent"); return lookup.promise;
    };
    const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); }, () => state);
    await follower.start();
    try {
      state = reducer(state, { type: "user", seq: 0, text: "question", submissionId: "submit" });
      const old = getTranscriptStore().installSlice(tab, path, {
        entries: [{ entryId: "m:old", turn: 1, order: 0, message: { role: "user", content: "old page", messageId: "old" }, refs: [] }],
        nextCursor: "", newerCursor: "next", hasOlder: false, hasNewer: true, startTurn: 1, endTurn: 1, totalTurns: 100,
        revision: 4, digest: "generation", stale: false,
      });
      state = reducer(state, { type: "history_replace", ...old });
      const resident = state.items;
      if (scenario === "event-first") {
        polls.shift()!.resolve({ protocolVersion: 2, subscription: tab, changes: [{ revision: 11, commitSeq: 4, durableSeq: 4, index: 0,
          event: { kind: "user_message", messageId: "sent", submissionId: "submit", source: "executor" } }], resetRequired: false });
        await microtasks();
        assert.equal(lookups, 0, "an identity event alone must not initiate a history read");
        assert.ok(state.localSubmissions.submit, "identity alone keeps the echo until the formal record");
      }
      polls.shift()!.resolve({ protocolVersion: 2, subscription: tab, changes: [{ revision: scenario === "event-first" ? 12 : 11, firstSeq: 5, commitSeq: 5, durableSeq: 5, index: 0,
        records: [{ role: "user", messageId: "sent", submissionId: lateBinding ? undefined : "submit", content: "question" }] }], resetRequired: false });
      await microtasks();
      if (lateBinding) {
        assert.ok(state.localSubmissions.submit);
        polls.shift()!.resolve({ protocolVersion: 2, subscription: tab, changes: [{ revision: 12, commitSeq: 5, durableSeq: 5, index: 0,
          event: { kind: "user_message", messageId: "sent", submissionId: "submit", source: "executor" } }], resetRequired: false });
        await microtasks();
        assert.equal(lookups, 1);
        if (scenario === "missing") {
          polls.shift()!.resolve({ protocolVersion: 2, subscription: tab, changes: [{ revision: 13, commitSeq: 5, durableSeq: 5, index: 0,
            event: { kind: "user_message", messageId: "sent", submissionId: "submit", source: "executor" } }], resetRequired: false });
          await microtasks();
          assert.equal(lookups, 1, "duplicate binding does not create a second in-flight read");
        }
        if (scenario === "stale") {
          state = reducer(state, { type: "reset" });
          state = reducer(state, { type: "user", seq: 0, text: "replacement", submissionId: "submit" });
          state = { ...state, localSubmissions: { submit: { ...state.localSubmissions.submit, messageId: "sent" } } };
        }
        lookup.resolve({ status: "ready", messages: scenario === "missing" ? [] : [{ role: "user", messageId: "sent" }] });
        await microtasks();
        if (scenario === "stale" || scenario === "missing") {
          assert.ok(state.localSubmissions.submit, "stale or inconclusive reads must retain the current echo");
          assert.equal(state.localSubmissions.submit.status, scenario === "stale" ? "sending" : "accepted");
          if (scenario === "missing") {
            assert.deepEqual(state.items, resident);
            polls.shift()!.resolve({ protocolVersion: 2, subscription: tab, changes: [{ revision: 14, firstSeq: 6, commitSeq: 6, durableSeq: 6, index: 0,
              records: [{ role: "user", messageId: "unrelated", content: "unrelated" }] }], resetRequired: false });
            await microtasks();
            assert.equal(lookups, 2, "new committed coverage permits one retry of an inconclusive read");
            assert.ok(state.localSubmissions.submit);
          }
          return;
        }
      } else assert.equal(lookups, 0, "ordinary formal handoff makes no extra history read");
      assert.equal(state.localSubmissionOrder.length, 0);
      assert.deepEqual(state.items, resident);
      assert.equal(Object.keys(state.visibleSubmissionHandoffs).length, 0);
      state = reducer(state, { type: "history_replace", items: [{ kind: "assistant", id: "m:later", text: "later", reasoning: "", streaming: false }],
        startTurn: 101, totalTurns: 101, hasOlder: true, hasNewer: false, revision: 6 });
      assert.equal(state.localSubmissionOrder.length, 0);
    } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
  });
}
function initial(subscription: string): TranscriptFollowResponse {
  return {
    protocolVersion: 2, subscription, changes: [], resetRequired: false,
    snapshot: {
      protocolVersion: 1, snapshotId: "cut", identity: { sessionId: subscription, runtimeEpoch: "epoch", rewriteEpoch: 0, headId: "" },
      projectionRevision: 10, coveredThroughSeq: 4, durableSeq: 4, records: [], activeRecords: [], activeAttempts: [],
      runtime: { status: "completed", pendingEvents: [], samplingCount: 0, toolCount: 0 }, before: 0, hasOlder: false, totalRecords: 1, totalTurns: 1, stale: false,
    },
    history: {
      status: "ready", snapshotSequence: 4, coverageSequence: 4, generation: "generation", totalTurns: 1, hasOlder: false, hasNewer: false,
      messages: [{ messageId: "answer", position: 0, version: 1, role: "assistant", eventSequence: 4, visibleTurn: 1,
        preview: "", contentRef: { digest: "canonical-message", bytes: 8192 } }],
    },
  };
}

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} running tab reconnect keeps its user before live reasoning when history and snapshot orders differ`, async () => {
  const tab = `running-order-${remote}`, path = `/session/${tab}`;
  const response = initial(tab);
  response.history!.totalTurns = 2;
  response.history!.messages = [
    { messageId: "previous", position: 10, version: 1, role: "assistant", eventSequence: 1, visibleTurn: 1,
      inline: { role: "assistant", content: "previous answer" } },
    { messageId: "user", position: 11, version: 1, role: "user", eventSequence: 2, visibleTurn: 2,
      preview: "make a tiger fly", inline: { role: "user", content: "make a tiger fly" } },
  ];
  response.snapshot!.totalRecords = 3;
  response.snapshot!.totalTurns = 2;
  response.snapshot!.runtime = { status: "in_progress", turnId: "current", pendingEvents: [], samplingCount: 1, toolCount: 0 };
  response.snapshot!.activeAttempts = [{ id: "attempt", messageId: "live", turnId: "current", nextIndex: 1 }];
  response.snapshot!.records = [
    { id: "m:previous", order: 0, message: { role: "assistant", messageId: "previous", content: "previous answer", historyTurn: 1 }, refs: [] },
    { id: "m:user", order: 1, message: { role: "user", messageId: "user", content: "make a tiger fly", historyTurn: 2, turnId: "current" }, refs: [] },
    { id: "m:live", order: 2, message: { role: "assistant", messageId: "live", content: "", reasoning: "thinking", historyTurn: 2, turnId: "current" }, refs: [] },
  ];
  const key = remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab";
  const pending = deferred<TranscriptFollowResponse>();
  let polls = 0;
  commands[key] = (_tab: string, request: FollowRequest) => request.close
    ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
    : request.subscription ? polls++ === 0 ? pending.promise : new Promise<TranscriptFollowResponse>(() => {}) : Promise.resolve(response);
  let state = initialState;
  const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
  const source = new ChatSource(tab);
  const assertVisibleOrder = () => {
    source.update({ items: state.items, running: state.running, hydrating: false, hasOlder: false, loadingOlder: false });
    const order = source.getOrderSnapshot();
    assert.ok(order.indexOf("m:user") >= 0 && order.indexOf("m:user") < order.indexOf("m:live:reasoning"),
      "the rendered reasoning stays in the user's turn");
  };
  try {
    await follower.start();
    assert.deepEqual(state.items.filter(item => item.kind === "user" || item.kind === "assistant").map(item => item.id),
      ["m:previous", "m:user", "m:live"]);
    assert.deepEqual(getTranscriptStore().peek(tab, path)?.items.map(item => item.id),
      ["m:previous", "m:user", "m:live"]);
    assertVisibleOrder();
    pending.resolve({ protocolVersion: 2, subscription: tab, resetRequired: false, changes: [{
      revision: 11, firstSeq: 5, commitSeq: 5, durableSeq: 5, index: 0,
      records: [{ role: "assistant", messageId: "live", turnId: "current", historyTurn: 2,
        content: "final answer", reasoning: "thinking" }],
      runtime: { status: "completed", turnId: "current", finalMessageId: "live", durationMs: 1000,
        pendingEvents: [], samplingCount: 1, toolCount: 0 },
    }] });
    await microtasks();
    assertVisibleOrder();
    assert.deepEqual(state.items.filter(item => item.kind === "user" || item.kind === "assistant").map(item => item.id),
      ["m:previous", "m:user", "m:live"]);
    assert.equal(state.running, false);
    assert.ok(state.items.some(item => item.kind === "assistant" && item.id === "m:live" && item.turnFinal));
  } finally { source.dispose(); follower.stop(); getTranscriptStore().evictTab(tab); }
});

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} native active prefix keeps its fixed-cut order when older pages fill the gap`, async () => {
  const tab = `native-prefix-order-${remote}`, path = `/session/${tab}`;
  const response = initial(tab);
  response.storageBackend = "legacy";
  delete response.history;
  const notice = (order: number) => ({ id: `notice:${order}`, order,
    message: { role: "notice", content: `step ${order}`, historyTurn: 1, turnId: "current" }, refs: [] });
  Object.assign(response.snapshot!, {
    before: 4, hasOlder: true, totalRecords: 5, totalTurns: 1,
    records: [notice(4)],
    activeRecords: [
      { id: "m:user", order: 0, message: { role: "user", messageId: "user", content: "build", historyTurn: 1, turnId: "current" }, refs: [] },
      { id: "m:live", order: 1, message: { role: "assistant", messageId: "live", content: "", reasoning: "thinking", historyTurn: 1, turnId: "current" }, refs: [] },
    ],
    activeAttempts: [{ id: "attempt", messageId: "live", turnId: "current", nextIndex: 1 }],
    runtime: { status: "in_progress", turnId: "current", pendingEvents: [], samplingCount: 1, toolCount: 0 },
  });
  const pending = deferred<TranscriptFollowResponse>();
  commands[remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab"] = (_tab: string, request: FollowRequest) => request.close
    ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
    : request.subscription ? pending.promise : Promise.resolve(response);
  commands[remote ? "RemoteTranscriptPageForTab" : "TranscriptPageForTab"] = async () => ({
    ...response.snapshot!, records: [notice(2), notice(3)], activeRecords: [], before: 2, hasOlder: true,
  });
  let state = initialState;
  const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
  try {
    await follower.start();
    const older = await getTranscriptStore().loadOlder(tab, path);
    assert.deepEqual(older?.items.map(item => item.id), ["m:user", "m:live", "he:notice:2", "he:notice:3", "he:notice:4"],
      "paging into the gap cannot move earlier output above the active user");
    assert.equal(state.historyTotalTurns, 1, "installing an existing active user does not invent a new turn");
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} snapshot aliases retain the authoritative active message body`, async () => {
  const tab = `active-alias-${remote}`, path = `/session/${tab}`;
  const response = initial(tab);
  response.history!.messages = [];
  response.snapshot!.runtime.status = "in_progress";
  response.snapshot!.activeAttempts = [{ id: "attempt", messageId: "answer", turnId: "current", nextIndex: 1 }];
  response.snapshot!.records = [{ id: "view:answer", order: 0,
    message: { role: "assistant", messageId: "answer", content: "old preview" }, refs: [] }];
  response.snapshot!.activeRecords = [{ id: "m:answer", order: 0,
    message: { role: "assistant", messageId: "answer", content: "current active body" }, refs: [] }];
  const pending = deferred<TranscriptFollowResponse>();
  commands[remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab"] = (_tab: string, request: FollowRequest) => request.close
    ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
    : request.subscription ? pending.promise : Promise.resolve(response);
  let state = initialState;
  const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
  try {
    await follower.start();
    assert.equal(state.items.length, 1, "aliases share one stable message node");
    assert.equal(state.items[0].kind === "assistant" && state.items[0].text, "current active body");
    assert.equal(state.live?.text, "current active body", "later deltas append to the authoritative active prefix");
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

for (const remote of [false, true]) for (const during of ["baseline", "baseline rejection", "delta", "retry", "load"] as const) {
  test(`${remote ? "remote" : "local"} stopping during ${during} fences current and future followers`, async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    desktopStub.emitServiceState({ phase: "ready", generation: "running" });
    const tab = `stopping-${remote}-${during}`;
    const requests: FollowRequest[] = [];
    const baseline = deferred<TranscriptFollowResponse>();
    const delta = deferred<TranscriptFollowResponse>();
    const readStarted = deferred<void>();
    commands[remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab"] = (_tab: string, request: FollowRequest) => {
      requests.push(request);
      if (request.close) return Promise.resolve({ protocolVersion: 2, changes: [] });
      readStarted.resolve();
      if (!request.subscription) return baseline.promise;
      return delta.promise;
    };
    let state = { ...initialState };
    const follower = new TranscriptSessionFollower(tab, "", remote, action => { state = reducer(state, action); });
    const starting = follower.start();
    if (during !== "load") await readStarted.promise;
    if (during === "delta" || during === "retry") {
      baseline.resolve(initial(tab)); await starting;
      if (during === "retry") {
        delta.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: true });
        await microtasks();
      }
    }
    const before = requests.length;
    desktopStub.emitServiceState({ phase: "stopping", generation: "running" });
    const visible = state;
    if (during === "baseline rejection") baseline.reject(new Error("stopping service rejected pending baseline"));
    else baseline.resolve(initial(tab));
    delta.resolve({ protocolVersion: 2, subscription: tab, changes: [{ revision: 11, commitSeq: 4, durableSeq: 4, index: 0, event: { kind: "text", messageId: "answer", text: "late" } }], resetRequired: false });
    await starting; await microtasks();
    t.mock.timers.tick(1000); await microtasks();
    const late = new TranscriptSessionFollower(`${tab}-late`, "", remote, () => { throw new Error("stopped service must not publish"); });
    await late.start(); await follower.start();
    assert.equal(requests.length, before, "no cleanup, retry or new baseline after stopping");
    assert.equal(state, visible, "a late response cannot update the retained transcript");
    follower.stop(); late.stop();
    desktopStub.emitServiceState({ phase: "ready", generation: "replacement" });
    getTranscriptStore().evictTab(tab);
  });
}

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} follower preserves an outer snapshot identity from an older peer`, async () => {
  const tab = `outer-record-${remote}`, path = `/session/${tab}`;
  const response = initial(tab);
  response.snapshot!.records = [
    { id: "view:older:1", order: 0, message: { role: "notice", content: "outer identity" }, refs: [] },
    { id: "tool:older-call", order: 1, message: { role: "tool", messageId: "tool-message", toolCallId: "older-call", toolName: "read_file", content: "result" }, refs: [] },
    { id: "", order: 2, message: { role: "notice", content: "legacy empty identity" }, refs: [] },
  ];
  response.snapshot!.totalRecords = 3;
  const key = remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab";
  const pending = deferred<TranscriptFollowResponse>();
  commands[key] = (_tab: string, request: FollowRequest) => request.close
    ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
    : request.subscription ? pending.promise : Promise.resolve(response);
  let state = initialState;
  const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
  try {
    await follower.start();
    assert.ok(state.items.some(item => item.kind === "notice" && item.text === "outer identity"));
    assert.ok(getTranscriptStore().peek(tab, path)?.items.some(item => item.id === "he:view:older:1"));
    assert.ok(state.items.some(item => item.kind === "tool" && item.id === "older-call"));
    assert.ok(state.items.some(item => item.kind === "notice" && item.text === "legacy empty identity"));
    assert.ok(!getTranscriptStore().peek(tab, path)?.items.some(item => item.id === "he:undefined"));
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

test("canonical tool history keeps its message identity when a tool call id is also present", async () => {
  const tab = "canonical-tool-identity", path = `/session/${tab}`;
  const response = initial(tab);
  response.history!.messages = [{
    messageId: "tool-message", position: 0, version: 1, role: "tool", eventSequence: 4, visibleTurn: 1,
    preview: "result", inline: { id: "tool-message", role: "tool", tool_call_id: "older-call", name: "read_file", content: "result" },
  }];
  commands.TranscriptFollowForTab = (_tab: string, request: FollowRequest) => Promise.resolve(request.close
    ? { protocolVersion: 2, subscription: tab, changes: [], resetRequired: false }
    : response);
  let state = initialState;
  const follower = new TranscriptSessionFollower(tab, path, false, action => { state = reducer(state, action); });
  try {
    await follower.start();
    assert.ok(getTranscriptStore().peek(tab, path)?.items.some(item => item.kind === "tool" && item.id === "older-call"));
    assert.ok(state.items.some(item => item.kind === "tool" && item.id === "older-call"));
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} follower coalesces a legacy tool alias before the final answer`, async () => {
  const tab = `legacy-tool-alias-${remote}`, path = `/session/${tab}`;
  const response = initial(tab);
  response.history!.messages = [
    { messageId: "user-1", position: 0, version: 1, role: "user", eventSequence: 1, visibleTurn: 1,
      preview: "make it", inline: { id: "user-1", role: "user", content: "make it" } },
    { messageId: "call-owner", position: 1, version: 1, role: "assistant", eventSequence: 2, visibleTurn: 1,
      preview: "", inline: { id: "call-owner", role: "assistant", content: "", tool_calls: [{ id: "call-1", name: "edit_file", arguments: '{"path":"blackhole.html"}' }] } },
    { messageId: "result-1", position: 2, version: 1, role: "tool", eventSequence: 3, visibleTurn: 1,
      preview: "written", inline: { id: "result-1", role: "tool", tool_call_id: "call-1", name: "edit_file", content: "written" } },
    { messageId: "final-1", position: 3, version: 1, role: "assistant", eventSequence: 4, visibleTurn: 1, turnFinal: true,
      preview: "done", inline: { id: "final-1", role: "assistant", content: "done" } },
  ];
  response.snapshot!.records = [{ id: "tool:call-1", order: 4,
    message: { recordId: "tool:call-1", role: "tool", toolCallId: "call-1", toolName: "edit_file", content: "written" }, refs: [] }];
  response.snapshot!.totalRecords = 5;
  const key = remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab";
  const pending = deferred<TranscriptFollowResponse>();
  commands[key] = (_tab: string, request: FollowRequest) => request.close
    ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
    : request.subscription ? pending.promise : Promise.resolve(response);
  let state = initialState;
  const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
  try {
    await follower.start();
    const tools = state.items.filter(item => item.kind === "tool" && item.id === "call-1");
    assert.equal(tools.length, 1, "formal result and tool:<callId> alias project one node");
    assert.equal(tools[0].kind === "tool" && tools[0].args, '{"path":"blackhole.html"}');
    assert.ok(state.items.findIndex(item => item.id === "call-1") < state.items.findIndex(item => item.id === "m:final-1"));
    const source = new ChatSource(tab);
    source.update({ items: state.items, running: false, hydrating: false, hasOlder: false, loadingOlder: false });
    await Promise.resolve();
    const process = source.getNodeSnapshot("m:user-1:process");
    assert.ok(process?.kind === "process" && process.foldable && process.collapsed,
      "normal completion folds after the duplicate trailing alias is removed");
    source.dispose();
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} follower joins a lazy formal result through its observation`, async () => {
  const tab = `lazy-formal-result-${remote}`, path = `/session/${tab}`;
  const response = initial(tab);
  response.history!.messages = [
    { messageId: "user", position: 0, version: 1, role: "user", eventSequence: 1, visibleTurn: 1,
      preview: "build", inline: { id: "user", role: "user", content: "build" } },
    { messageId: "owner", position: 1, version: 1, role: "assistant", eventSequence: 2, visibleTurn: 1,
      preview: "", inline: { id: "owner", role: "assistant", content: "", tool_calls: [{ id: "lazy", name: "write_file", arguments: '{"path":"lazy.html"}' }] },
      toolObservations: { lazy: { state: "completed", messageId: "result", version: 1, contentRef: { digest: "body", bytes: 8192 } } } },
    { messageId: "result", position: 2, version: 1, role: "tool", eventSequence: 3, visibleTurn: 1,
      preview: "written", contentRef: { digest: "body", bytes: 8192 } },
    { messageId: "final", position: 3, version: 1, role: "assistant", eventSequence: 4, visibleTurn: 1, turnFinal: true,
      preview: "done", inline: { id: "final", role: "assistant", content: "done" } },
  ];
  response.snapshot!.records = [{ id: "tool:lazy", order: 4,
    message: { recordId: "tool:lazy", role: "tool", toolCallId: "lazy", toolName: "write_file", content: "written" }, refs: [] }];
  response.snapshot!.totalRecords = 5;
  const key = remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab";
  const pending = deferred<TranscriptFollowResponse>();
  commands[key] = (_tab: string, request: FollowRequest) => request.close
    ? Promise.resolve({ protocolVersion: 2, subscription: tab, changes: [], resetRequired: false })
    : request.subscription ? pending.promise : Promise.resolve(response);
  let state = initialState;
  const follower = new TranscriptSessionFollower(tab, path, remote, action => { state = reducer(state, action); });
  try {
    await follower.start();
    const tools = state.items.filter((item): item is Extract<import("../lib/useController").Item, { kind: "tool" }> => item.kind === "tool");
    assert.equal(tools.length, 1);
    assert.equal(tools[0].id, "lazy");
    assert.equal(tools[0].args, '{"path":"lazy.html"}');
    assert.ok(state.items.findIndex(item => item.id === "lazy") < state.items.findIndex(item => item.id === "m:final"));
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} malformed snapshot leaves the resident store unchanged`, async () => {
  const tab = `invalid-record-${remote}`, path = `/session/${tab}`;
  getTranscriptStore().installSlice(tab, path, {
    entries: [{ entryId: "m:resident", turn: 1, order: 0, message: { role: "assistant", messageId: "resident", content: "resident" }, refs: [] }],
    nextCursor: "", hasOlder: false, newerCursor: "", hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "resident", stale: false,
  });
  const response = initial(tab);
  response.snapshot!.records = [{ id: "", order: 0, message: { role: "notice", content: "invalid" },
    refs: [{ snapshotId: "cut", recordId: "", path: ["content"], bytes: 100 }] }];
  response.snapshot!.totalRecords = 1;
  const key = remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab";
  commands[key] = (_tab: string, request: FollowRequest) => Promise.resolve(request.close
    ? { protocolVersion: 2, subscription: tab, changes: [], resetRequired: false }
    : response);
  const follower = new TranscriptSessionFollower(tab, path, remote, () => undefined);
  try {
    await assert.rejects(follower.start(), /transcript snapshot content identity missing/);
    const resident = getTranscriptStore().peek(tab, path);
    assert.equal(resident?.digest, "resident");
    assert.ok(resident?.items.some(item => item.id === "m:resident"));
    assert.ok(!resident?.items.some(item => item.id === "he:undefined"));
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

test("reducer rejection does not commit a prepared transcript replacement", async () => {
  const tab = "reducer-reject", path = `/session/${tab}`;
  getTranscriptStore().installSlice(tab, path, {
    entries: [{ entryId: "m:resident", turn: 1, order: 0, message: { role: "assistant", messageId: "resident", content: "resident" }, refs: [] }],
    nextCursor: "", hasOlder: false, newerCursor: "", hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "resident", stale: false,
  });
  commands.TranscriptFollowForTab = (_tab: string, request: FollowRequest) => Promise.resolve(request.close
    ? { protocolVersion: 2, subscription: tab, changes: [], resetRequired: false }
    : initial(tab));
  const follower = new TranscriptSessionFollower(tab, path, false, action => {
    if (action.type === "transcript_v2_snapshot") throw new Error("reducer rejected snapshot");
  });
  try {
    await assert.rejects(follower.start(), /reducer rejected snapshot/);
    const resident = getTranscriptStore().peek(tab, path);
    assert.equal(resident?.digest, "resident");
    assert.ok(resident?.items.some(item => item.id === "m:resident"));
  } finally { follower.stop(); getTranscriptStore().evictTab(tab); }
});

test("snapshot rejects duplicate durable identities and mismatched content references", async () => {
  const cases: Array<{ name: string; records: NonNullable<TranscriptFollowResponse["snapshot"]>["records"]; pattern: RegExp }> = [
    { name: "duplicate", records: [
      { id: "same", order: 0, message: { role: "notice", recordId: "same", content: "first" }, refs: [] },
      { id: "same", order: 1, message: { role: "notice", recordId: "same", content: "second" }, refs: [] },
    ], pattern: /duplicate transcript snapshot record identity/ },
    { name: "content-ref", records: [{ id: "owner", order: 0, message: { role: "notice", recordId: "owner", content: "preview" },
      refs: [{ snapshotId: "cut", recordId: "different", path: ["content"], bytes: 100 }] }], pattern: /content identity mismatch/ },
    { name: "embedded-record", records: [{ id: "owner", order: 0, message: { role: "notice", recordId: "different", content: "preview" }, refs: [] }],
      pattern: /record identity mismatch/ },
  ];
  for (const fixture of cases) {
    const tab = `invalid-${fixture.name}`;
    const response = initial(tab);
    response.snapshot!.records = fixture.records;
    response.snapshot!.totalRecords = fixture.records.length;
    commands.TranscriptFollowForTab = (_tab: string, request: FollowRequest) => Promise.resolve(request.close
      ? { protocolVersion: 2, subscription: tab, changes: [], resetRequired: false }
      : response);
    const follower = new TranscriptSessionFollower(tab, "", false, () => undefined);
    try { await assert.rejects(follower.start(), fixture.pattern); }
    finally { follower.stop(); getTranscriptStore().evictTab(tab); }
  }
});

for (const remote of [false, true]) {
  test(`${remote ? "remote" : "local"} follower retains an empty canonical body reference as a loadable assistant node`, async () => {
    const tab = remote ? "follow-ref-remote" : "follow-ref-local";
    let state = initialState;
    const requests: FollowRequest[] = [];
    const poll = deferred<TranscriptFollowResponse>();
    const read = async (tabId: string, request: FollowRequest): Promise<TranscriptFollowResponse> => {
      assert.equal(tabId, tab); requests.push(request);
      if (request.close) return { protocolVersion: 2, subscription: tab, changes: [], resetRequired: false };
      if (!request.subscription) return initial(tab);
      return poll.promise;
    };
    const key = remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab";
    const wrong = remote ? "TranscriptFollowForTab" : "RemoteTranscriptFollowForTab";
    commands[key] = read;
    commands[wrong] = () => { throw new Error("cross-host fallback is forbidden"); };
    commands.SendForTab = () => { throw new Error("history recovery must not invoke the model"); };
    commands.RemoteSendForTab = commands.SendForTab;
    const follower = new TranscriptSessionFollower(tab, `/session/${tab}`, remote, action => { state = reducer(state, action); });
    try {
      await follower.start();
      const assistant = state.items.find(item => item.kind === "assistant");
      assert.ok(assistant, "empty inline preview with a canonical ref must not disappear");
      assert.equal(assistant.id, "m:answer");
      assert.equal(getTranscriptStore().hasContentReference(tab, "m:answer", "content"), true);
      assert.equal(state.running, false);
      assert.equal(state.transcriptProtocol, 2);
      assert.equal(requests.length, 2, "follow starts only after initial installation");
    } finally { follower.stop(); }
    await microtasks();
    assert.ok(requests.some(request => request.close && request.subscription === tab));
  });
}

test("stopped session follower cannot install a delayed baseline into its replaced tab", async () => {
  const delayed = deferred<TranscriptFollowResponse>();
  const started = deferred<void>();
  const requests: FollowRequest[] = [];
  commands.TranscriptFollowForTab = (_tab: string, request: FollowRequest) => {
    requests.push(request);
    started.resolve();
    return request.close ? Promise.resolve({ protocolVersion: 2, subscription: "stale", changes: [], resetRequired: false }) : delayed.promise;
  };
  let state = initialState;
  const follower = new TranscriptSessionFollower("stale-tab", "/session/stale", false, action => { state = reducer(state, action); });
  const loading = follower.start(); await started.promise; follower.stop(); delayed.resolve(initial("stale")); await loading; await microtasks();
  assert.equal(state.items.length, 0);
  assert.ok(requests.some(request => request.close && request.subscription === "stale"));
});

test("stopping before lazy follow startup prevents a backend subscription", async () => {
  let requests = 0;
  commands.TranscriptFollowForTab = async () => { requests++; return initial("cancelled-load"); };
  const follower = new TranscriptSessionFollower("cancelled-load", "/session/cancelled-load", false, () => {
    assert.fail("cancelled module load cannot publish state");
  });
  const loading = follower.start();
  follower.stop();
  await loading;
  assert.equal(requests, 0);
});

for (const remote of [false, true]) test(`${remote ? "remote" : "local"} active reference is complete before suffix polling`, async () => {
  const response = initial(`prefix-${remote}`);
  response.snapshot!.runtime.status = "in_progress";
  response.snapshot!.activeAttempts = [{ id: "attempt", messageId: "answer", turnId: "turn", nextIndex: 1 }];
  response.snapshot!.records = [{ id: "m:answer", order: 0, message: { role: "assistant", messageId: "answer", content: "truncated" },
    refs: [{ snapshotId: "cut", recordId: "m:answer", path: ["content"], bytes: 20 }] }];
  const content = deferred<{ data: string; nextOffset: number; done: boolean; stale: boolean }>();
  const poll = deferred<TranscriptFollowResponse>();
  let polls = 0;
  commands[remote ? "RemoteTranscriptContentForTab" : "TranscriptContentForTab"] = () => content.promise;
  commands[remote ? "RemoteTranscriptFollowForTab" : "TranscriptFollowForTab"] = (_tab: string, request: FollowRequest) => {
    if (request.close) return Promise.resolve({ protocolVersion: 2, changes: [], resetRequired: false, subscription: response.subscription });
    if (!request.subscription) return Promise.resolve(response);
    polls++; return poll.promise;
  };
  let state = initialState;
  const follower = new TranscriptSessionFollower(`prefix-${remote}`, "", remote, action => { state = reducer(state, action); });
  const starting = follower.start();
  await microtasks();
  assert.equal(polls, 0);
  assert.equal(state.items.length, 0, "partial baseline is not published");
  content.resolve({ data: "complete prefix", nextOffset: 15, done: true, stale: false });
  await starting;
  assert.equal(state.live?.text, "complete prefix");
  assert.equal(polls, 1);
  follower.stop();
});

test("v2 keeps deferred bodies through subsequent samples and attaches terminal time by backend message identity", () => {
  let state: import("../lib/useController").State = { ...initialState, transcriptProtocol: 2, running: true, activeTurnId: "turn",
    items: [
      { kind: "assistant" as const, id: "m:deferred", text: "", reasoning: "", streaming: false },
      { kind: "assistant" as const, id: "m:final", text: "answer", reasoning: "thinking", streaming: false },
    ] };
  state = reducer(state, { type: "event", e: { kind: "text", messageId: "next", text: "later sample" } });
  assert.ok(state.items.some(item => item.id === "m:deferred"), "text frames must not remove unloaded body owners");
  state = reducer(state, { type: "event", e: { kind: "turn_done", turnId: "turn" } });
  state = reducer(state, { type: "transcript_runtime", runtime: { turnId: "turn", status: "completed",
    finalMessageId: "final", durationMs: 933524, samplingCount: 72, toolCount: 72, pendingEvents: [] } });
  const final = state.items.find(item => item.id === "m:final");
  assert.ok(final?.kind === "assistant");
  assert.equal(final.turnDurationMs, 933524);
  assert.equal(final.turnFinal, true);
  assert.ok(state.items.some(item => item.id === "m:deferred"), "terminal must retain deferred body owners");
  const later = state.items.find(item => item.id === "m:next");
  assert.ok(later?.kind === "assistant");
  assert.equal(later.turnDurationMs, undefined, "array-tail sample is not the final reply");
});
