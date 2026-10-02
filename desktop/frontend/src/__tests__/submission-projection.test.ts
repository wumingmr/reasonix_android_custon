import assert from "node:assert/strict";
import { initialState, reducer } from "../lib/useController";
import { entryToRecord, convertRecord } from "../lib/transcriptRecordProjection";
import { toolPresentation, type ToolItem } from "../lib/chatToolPresentation";
import { ChatSource } from "../lib/chatViewSource";

const record = entryToRecord({ entryId: "m:backend-user", turn: 1, order: 0, refs: [], message: {
  role: "user", content: "same input", messageId: "backend-user", submissionId: "submit-1",
} });
const converted = convertRecord(record, { records: [record], indexOf: new Map([[record.entryId, 0]]),
  toolResultOwners: new Map(), toolCallOwners: new Map(), toolCallDisplayIds: new Map(), toolDisplayIds: new Map(),
  toolIdentityConflicts: new Set(), suppressedToolResults: new Set(), claimedToolResults: new Set() }, new Set());
const projection = { items: converted.items, removeIds: [], startTurn: 0, endTurn: 1, totalTurns: 1,
  hasOlder: false, hasNewer: false, revision: 1, revisionKnown: true, digest: "cut" };
for (const eventFirst of [true, false]) {
  let state = reducer({ ...initialState, transcriptProtocol: 2 }, { type: "user", seq: 0, text: "same input", submissionId: "submit-1" });
  const event = { type: "event" as const, e: { kind: "user_message" as const, source: "executor" as const, messageId: "backend-user", submissionId: "submit-1", text: "same input" } };
  if (eventFirst) {
    state = reducer(state, event);
    assert.equal(state.items.length, 0, "v2 admission binds identity without installing a durable row");
    assert.equal(state.localSubmissions["submit-1"]?.messageId, "backend-user");
  }
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection });
  if (!eventFirst) state = reducer(state, event);
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection });
  const users = state.items.filter(item => item.kind === "user");
  assert.equal(users.length, 1);
  assert.equal(users[0].id, "m:backend-user");
  assert.equal(users[0].messageId, "backend-user");
  assert.equal(state.localSubmissionOrder.length, 0);
  state = reducer(state, { type: "user", seq: 1, text: "same input", submissionId: "submit-2" });
  assert.equal(state.items.filter(item => item.kind === "user").length, 1);
  assert.equal(state.localSubmissionOrder.length, 1);
}

{
  let state = reducer({ ...initialState, transcriptProtocol: 2 }, { type: "user", seq: 0, text: "question", submissionId: "shared-submit" });
  const first = { kind: "user" as const, id: "m:first", messageId: "first", submissionId: "shared-submit", text: "question" };
  const second = { kind: "user" as const, id: "m:second", messageId: "second", submissionId: "shared-submit", text: "question" };
  state = reducer(state, { type: "transcript_records", confirmedUsers: [], projection: { ...projection, items: [first, second] } });
  assert.deepEqual(state.items.filter(item => item.kind === "user").map(item => item.id), ["m:first", "m:second"]);
  assert.equal(state.localSubmissionOrder.length, 0, "one local echo is consumed at most once");
}

{
  const source = new ChatSource("bounded-display-keys");
  const local = { submissionId: "display-submit", localId: "u0", text: "question", createdAt: 1, sequence: 0, status: "sending" as const };
  source.update({ items: [], localSubmissions: [local], running: true, hydrating: false, hasOlder: false, loadingOlder: false });
  assert.ok(source.getOrderSnapshot().includes("submission:display-submit"));
  source.update({ items: [{ kind: "user", id: "m:display", messageId: "display", submissionId: "display-submit", text: "question" }],
    localSubmissions: [], running: true, hydrating: false, hasOlder: false, loadingOlder: false });
  assert.ok(source.getOrderSnapshot().includes("submission:display-submit"), "canonical handoff inherits the mounted presentation key");
  source.update({ items: [], localSubmissions: [], running: false, hydrating: false, hasOlder: false, loadingOlder: false });
  source.update({ items: [{ kind: "user", id: "m:reloaded", messageId: "reloaded", submissionId: "display-submit", text: "question" }],
    localSubmissions: [], running: false, hydrating: false, hasOlder: false, loadingOlder: false });
  assert.ok(source.getOrderSnapshot().includes("m:reloaded"), "reloaded history uses a fresh durable presentation key after reclaim");
  source.dispose();
}
const shell: ToolItem = { kind: "tool", id: "call", name: "bash", args: "{}", status: "done", readOnly: false };
assert.equal(toolPresentation(shell).label, "chat.unknown");
assert.equal(toolPresentation({ ...shell, status: "stopped" }).dot, "warning");
assert.equal(toolPresentation({ ...shell, execution: { kind: "shell", supportsAndAnd: true, state: "timed_out", exitCode: 0 } }).label, "chat.timedOut");
assert.equal(toolPresentation({ ...shell, execution: { kind: "shell", supportsAndAnd: true, state: "completed", exitCode: 0 } }).dot, "done");
console.log("submission identity ordering and authoritative tool states passed");
