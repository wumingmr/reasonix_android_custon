import assert from "node:assert/strict";
import test from "node:test";
import { initialState, reducer, type Item } from "../lib/useController";
import { ChatSource } from "../lib/chatViewSource";
import { orderedLocalSubmissions } from "../lib/localSubmissionState";
import type { TranscriptSnapshot } from "../lib/transcriptProtocol";

const user = (messageId: string, submissionId?: string): Item => ({ kind: "user", id: `m:${messageId}`, messageId, submissionId, text: "question" });
const projection = (items: Item[]) => ({ items, removeIds: [], startTurn: 1, endTurn: 1, totalTurns: 1,
  hasOlder: false, hasNewer: false, revision: 1, revisionKnown: true, digest: "cut" });
const sent = () => reducer({ ...initialState, transcriptProtocol: 2 }, { type: "user", seq: 0, text: "question", submissionId: "send" });
const event = (messageId = "durable") => ({ type: "event" as const, e: { kind: "user_message" as const, source: "executor" as const, submissionId: "send", messageId } });
const input = { running: true, hydrating: false, hasOlder: false, loadingOlder: false };

test("a batched bound handoff reserves its displayed key before a conflicting canonical row", () => {
  const source = new ChatSource("batched-conflict");
  let state = sent();
  source.update({ ...input, items: [], localSubmissions: orderedLocalSubmissions(state) });
  const original = source.getOrderSnapshot().find(key => source.getNodeSnapshot(key)?.kind === "user");
  state = reducer(state, event("bound"));
  state = reducer(state, { type: "transcript_records", confirmedUsers: [],
    projection: projection([user("conflict", "send"), user("bound", "send")]) });
  source.update({ ...input, items: state.items, localSubmissions: orderedLocalSubmissions(state), visibleSubmissionHandoffs: state.visibleSubmissionHandoffs });
  const rows = source.getOrderSnapshot().map(key => source.getNodeSnapshot(key)).filter(node => node?.kind === "user");
  assert.equal(rows.find(node => node?.item.messageId === "bound")?.key, original);
  assert.equal(rows.find(node => node?.item.messageId === "conflict")?.key, "m:conflict");
  source.dispose();
});

test("offscreen formal confirmation retires an echo without changing the reader page", () => {
  const old = user("old");
  const state = { ...sent(), items: [old], historyHasNewer: true };
  const action = { type: "transcript_records" as const, projection: { ...projection([old]), hasNewer: true },
    confirmedUsers: [{ messageId: "durable", submissionId: "send" }] };
  const next = reducer(state, action);
  assert.equal(next.localSubmissionOrder.length, 0);
  assert.equal(next.items[0], old);
  assert.equal(next.localSubmissionSendRevision, state.localSubmissionSendRevision);
});

for (const order of ["erc", "ecr", "rec", "rce", "cer", "cre"]) test(`handoff permutation ${order}`, () => {
  let state = sent();
  for (const step of order) {
    state = step === "e" ? reducer(state, event()) : step === "c" ? reducer(state, { type: "send_confirmed", submissionId: "send" })
      : reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable", "send")]) });
    assert.equal(state.items.filter(x => x.kind === "user").length + state.localSubmissionOrder.length, 1);
  }
  assert.equal(state.items[0].id, "m:durable");
});

test("records without submission identity are reconciled when the binding event arrives later", () => {
  let state = reducer(sent(), { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable")]) });
  state = reducer(state, event());
  assert.equal(state.localSubmissionOrder.length, 0);
});

test("batched binding and formal install preserve the mounted display key", () => {
  const source = new ChatSource("batched-handoff");
  let state = sent();
  source.update({ ...input, items: [], localSubmissions: orderedLocalSubmissions(state) });
  const before = source.getOrderSnapshot();
  state = reducer(reducer(state, event()), { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable")]) });
  const next = { ...input, items: state.items, localSubmissions: orderedLocalSubmissions(state),
    visibleSubmissionHandoffs: (state as typeof state & { visibleSubmissionHandoffs: Record<string, { submissionId: string }> }).visibleSubmissionHandoffs };
  source.update(next);
  assert.deepEqual(source.getOrderSnapshot(), before);
  source.dispose();
});

test("a bound echo cannot be rebound or hidden by a different message sharing the submission", () => {
  let state = reducer(sent(), event("first"));
  state = reducer(state, event("other"));
  assert.equal(state.localSubmissions.send.messageId, "first");
  const source = new ChatSource("conflict");
  source.update({ ...input, items: [user("other", "send")], localSubmissions: orderedLocalSubmissions(state) });
  assert.equal(source.getOrderSnapshot().filter(key => source.getNodeSnapshot(key)?.kind === "user").length, 2);
  source.dispose();
});

test("a reclaimed anchor does not insert a pending bubble before unrelated history", () => {
  const source = new ChatSource("missing-anchor");
  const state = reducer({ ...initialState, items: [user("old-anchor")] }, { type: "user", seq: 0, text: "question", submissionId: "send" });
  source.update({ ...input, items: [user("unrelated")], localSubmissions: orderedLocalSubmissions(state) });
  assert.equal(source.getOrderSnapshot().includes("submission:send"), false);
  source.dispose();
});

test("a session-start echo stays outside a window that no longer contains the start", () => {
  const source = new ChatSource("reclaimed-start");
  source.update({ ...input, items: [user("later")], hasOlder: true, historyStartTurn: 500,
    localSubmissions: orderedLocalSubmissions(sent()) });
  assert.equal(source.getOrderSnapshot().includes("submission:send"), false);
  source.dispose();
});

test("late management acknowledgement removes its own echo after request acceptance", () => {
  let state = reducer(sent(), { type: "send_confirmed", submissionId: "send" });
  state = reducer(state, { type: "user", seq: 1, text: "next", submissionId: "next" });
  state = reducer(state, { type: "management_confirmed", submissionId: "send" });
  assert.equal(state.localSubmissions.send, undefined);
  assert.ok(state.localSubmissions.next);
  assert.equal(state.pendingSubmissionId, "next");
  assert.equal(state.running, true);
});

test("old rejection cannot stop a newer accepted submission awaiting its turn identity", () => {
  let state = reducer(sent(), { type: "turn_submit_unknown", submissionId: "send", error: "timeout" });
  state = reducer(state, { type: "user", seq: 1, text: "next", submissionId: "next" });
  state = reducer(state, { type: "send_confirmed", submissionId: "next" });
  state = reducer(state, { type: "transcript_connection", status: "connected" });
  state = reducer(state, { type: "turn_submit_unknown", submissionId: "send", error: "late timeout" });
  assert.equal(state.transcriptConnection, "connected", "old timeout cannot disconnect the newer submission");
  state = reducer(state, { type: "send_failed", submissionId: "send", error: "late rejection" });
  assert.equal(state.localSubmissions.send.status, "failed");
  assert.equal(state.localSubmissions.next.status, "accepted");
  assert.equal(state.running, true);
});

for (const status of ["send_failed", "turn_submit_unknown"] as const) test(`${status} converges on a late formal record`, () => {
  let state = reducer(sent(), { type: status, submissionId: "send", error: "fixture" });
  assert.equal(state.localSubmissions.send.status, status === "send_failed" ? "failed" : "unknown");
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable", "send")]) });
  assert.equal(state.localSubmissionOrder.length, 0);
  state = reducer(state, { type: status, submissionId: "send", error: "late" });
  assert.equal(state.localSubmissionOrder.length, 0);
});

test("remove plus upsert is atomic and reclaimed rows cannot be revived by a content patch", () => {
  let state = reducer(sent(), { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable", "send")]) });
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: { ...projection([user("durable", "send")]), removeIds: ["m:durable"] } });
  assert.equal(state.items.length, 1);
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: { ...projection([]), removeIds: ["m:durable"] } });
  assert.equal(Object.keys(state.visibleSubmissionHandoffs).length, 0);
  state = reducer(state, { type: "history_items_patch", patches: { "m:durable": user("durable", "send") } });
  assert.equal(state.items.length, 0);
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable", "send")]) });
  assert.equal(state.items.length, 1);
  assert.equal(Object.keys(state.visibleSubmissionHandoffs).length, 0);
});

test("a delayed user record precedes its already streaming answer", () => {
  let state = reducer(sent(), { type: "event", e: { kind: "text", messageId: "answer", turnId: "turn", text: "answer" } });
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([{ ...user("durable", "send"), turnId: "turn" }]) });
  assert.deepEqual(state.items.map(item => item.id), ["m:durable", "m:answer"]);
});

test("a start-of-session user record precedes streamed output without a turn id", () => {
  let state = reducer(sent(), { type: "event", e: { kind: "text", messageId: "answer", text: "answer" } });
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable", "send")]) });
  assert.deepEqual(state.items.map(item => item.id), ["m:durable", "m:answer"]);
});

test("a later turn's user record stays after its anchor and before the new live output", () => {
  let state = reducer(sent(), { type: "transcript_records", confirmedUsers: [], projection: projection([user("first", "send")]) });
  state = reducer(state, { type: "user", seq: 1, submissionId: "next", text: "next question" });
  state = reducer(state, { type: "event", e: { kind: "text", messageId: "answer", text: "already streaming" } });
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([user("first", "send"), user("second", "next")]) });
  assert.deepEqual(state.items.map(item => item.id), ["m:first", "m:second", "m:answer"]);
});

test("a reclaimed submission anchor does not place the user row before unrelated history", () => {
  let state = reducer({ ...initialState, items: [user("old")] }, { type: "user", seq: 1, submissionId: "send", text: "new" });
  state = { ...state, items: [user("other")], transcriptProjectedIds: ["m:other"], historyHasOlder: true, historyStartTurn: 4 };
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([user("other"), user("new", "send")]) });
  assert.deepEqual(state.items.map(item => item.id), ["m:other", "m:new"]);
});

test("formal output published ahead of its user moves behind that user", () => {
  const state = reducer(sent(), { type: "transcript_records", confirmedUsers: [], projection: projection([
    { kind: "assistant", id: "m:answer", text: "answer", reasoning: "", streaming: false, turnId: "turn" },
    { ...user("durable", "send"), turnId: "turn" },
  ]) });
  assert.deepEqual(state.items.map(item => item.id), ["m:durable", "m:answer"]);
});

test("a projection update without a turn id keeps the resident turn id", () => {
  let state = reducer(sent(), { type: "event", e: { kind: "text", messageId: "answer", turnId: "turn", text: "answer" } });
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([
    { kind: "assistant", id: "m:answer", text: "answer", reasoning: "", streaming: false },
  ]) });
  const assistant = state.items.find(item => item.id === "m:answer");
  assert.equal(assistant?.kind === "assistant" ? assistant.turnId : undefined, "turn");
});

test("a later event stamps the live answer that arrived without a turn id", () => {
  let state = reducer(sent(), { type: "event", e: { kind: "text", messageId: "answer", text: "answer" } });
  assert.equal(state.items.find(item => item.kind === "assistant")?.turnId, undefined);
  state = reducer(state, { type: "event", e: { kind: "stream_attempt", messageId: "answer", turnId: "turn", streamAttempt: { id: "sa", action: "begin", attempt: 1, max: 1 } } });
  assert.equal(state.items.find(item => item.id === "m:answer")?.turnId, "turn");
});

test("a stream batch stamps the active turn onto the live answer", () => {
  let state = reducer(sent(), { type: "event", e: { kind: "turn_started", status: "in_progress" } });
  assert.equal(state.items.find(item => item.kind === "assistant")?.turnId, undefined);
  state = reducer({ ...state, activeTurnId: "turn" }, { type: "stream_batch", segments: [{ kind: "reasoning", delta: "想" }] });
  assert.equal(state.items.find(item => item.kind === "assistant")?.turnId, "turn");
  assert.equal(state.live?.reasoning, "想");
});

test("reset clears echoes and presentation mappings", () => {
  let state = reducer(sent(), { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable", "send")]) });
  state = reducer(state, { type: "user", seq: 1, submissionId: "pending", text: "pending" });
  state = reducer(state, { type: "reset" });
  assert.equal(state.localSubmissionOrder.length, 0);
  assert.equal(Object.keys(state.visibleSubmissionHandoffs).length, 0);
});

test("late unknown transport result cannot downgrade terminal delivery evidence", () => {
  const state = reducer(sent(), { type: "event", e: { kind: "turn_done", submissionId: "send", checkpointTurn: 1 } });
  assert.equal(reducer(state, { type: "turn_submit_unknown", submissionId: "send", error: "late timeout" }), state);
});

test("a batch hands off once and retains all distinct formal identities", () => {
  const state = reducer(sent(), { type: "transcript_records", confirmedUsers: [],
    projection: projection([user("first", "send"), user("second", "send")]) });
  assert.deepEqual(state.items.map(item => item.id), ["m:first", "m:second"]);
  assert.deepEqual(state.visibleSubmissionHandoffs, { first: { submissionId: "send" } });
  const source = new ChatSource("multiple-formal");
  source.update({ ...input, items: [], localSubmissions: orderedLocalSubmissions(sent()) });
  source.update({ ...input, items: state.items, visibleSubmissionHandoffs: state.visibleSubmissionHandoffs });
  assert.deepEqual(source.getOrderSnapshot().filter(key => source.getNodeSnapshot(key)?.kind === "user"), ["submission:send", "m:second"]);
  source.dispose();
});

test("legacy installs preserve handoffs for other resident formal messages", () => {
  let state = reducer({ ...sent(), transcriptProtocol: undefined }, event());
  assert.deepEqual(state.visibleSubmissionHandoffs, { durable: { submissionId: "send" } });
  state = reducer(state, { type: "event", e: { kind: "user_message", source: "executor", messageId: "another", text: "other" } });
  assert.deepEqual(state.visibleSubmissionHandoffs, { durable: { submissionId: "send" } });
});

test("binding and admission cannot erase a failed echo before its formal confirmation", () => {
  let state = reducer(sent(), { type: "send_failed", submissionId: "send", error: "rejected" });
  state = reducer(state, event());
  state = reducer(state, { type: "turn_admitted", submissionId: "send", turnId: "turn" });
  assert.equal(state.localSubmissions.send.status, "failed");
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: projection([user("durable")]) });
  assert.equal(state.localSubmissionOrder.length, 0);
});

test("same-session recovery retains pending echoes, a different snapshot owner clears them", () => {
  const snapshot: TranscriptSnapshot = { protocolVersion: 1, snapshotId: "snapshot", identity: {
    sessionId: "session-a", runtimeEpoch: "runtime", rewriteEpoch: 0, headId: "" }, projectionRevision: 1,
    coveredThroughSeq: 1, records: [], activeRecords: [], activeAttempts: [], runtime: { status: "completed", pendingEvents: [] },
    before: 0, hasOlder: false, totalRecords: 0, totalTurns: 0, stale: false };
  let state = reducer(initialState, { type: "transcript_snapshot", snapshot });
  state = reducer(state, { type: "user", seq: 0, submissionId: "send", text: "question" });
  state = reducer(state, { type: "transcript_snapshot", snapshot });
  assert.ok(state.localSubmissions.send);
  const offscreenSnapshot = { ...snapshot, totalRecords: 1, records: [{ id: "m:durable", order: 0, refs: [],
    message: { role: "user" as const, messageId: "durable", submissionId: "send", content: "question" } }] };
  const confirmed = reducer(state, { type: "transcript_v2_snapshot", snapshot: offscreenSnapshot, projection: projection([user("old")]) });
  assert.equal(confirmed.localSubmissionOrder.length, 0, "snapshot confirmations precede window filtering");
  assert.deepEqual(confirmed.items.map(item => item.id), ["m:old"]);
  assert.equal(Object.keys(confirmed.visibleSubmissionHandoffs).length, 0);
  const generation = state.sessionGen;
  state = reducer(state, { type: "transcript_snapshot", snapshot: { ...snapshot, identity: { ...snapshot.identity, sessionId: "session-b" } } });
  assert.equal(state.localSubmissionOrder.length, 0);
  assert.equal(Object.keys(state.visibleSubmissionHandoffs).length, 0);
  assert.equal(state.pendingSubmissionId, undefined);
  assert.equal(state.sessionGen, generation + 1);
  assert.equal(state.running, false);
});
