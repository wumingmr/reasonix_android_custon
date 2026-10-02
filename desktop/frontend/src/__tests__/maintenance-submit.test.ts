import assert from "node:assert/strict";
import test from "node:test";
import { createTurnSubmissionId, historyMessagesToItems, initialState, reducer, type State } from "../lib/useController";
import { submitTurn, type ManagementReceipt } from "../lib/turnSubmit";
import type { AppBindings } from "../lib/bridge";
import type { RuntimeState } from "../lib/runtimeStateStore";
import { canonicalMessage } from "../lib/canonicalTranscriptBackend";
import { isCompactCommand, isCompactSubmission } from "../lib/sessionMaintenanceOperation";
import { TranscriptStore, type TranscriptBackend } from "../lib/transcriptStore";
import type { TranscriptSnapshot } from "../lib/transcriptProtocol";

const operation = { operationId: "compact-A", kind: "compact", activity: "running", status: "running", operationRevision: 1, runtimeEpoch: "epoch-A" };
const runtime = (revision: number, maintenance: RuntimeState["maintenance"]): RuntimeState => ({
  schemaVersion: 1, projectionEpoch: "projection-A", runtimeEpoch: "epoch-A", activityRevision: revision, revision,
  phase: maintenance ? "executing" : "idle", running: Boolean(maintenance), turnId: "", turnStatus: "", turnEventSeq: 0,
  pendingPrompt: false, cancelRequested: false, cancellable: Boolean(maintenance), backgroundJobs: 0, activity: maintenance ? "maintenance" : "", maintenance,
});
const pending = (state = initialState, submissionId = "submit") => reducer(state, { type: "user", text: "/compact", seq: state.seq, submissionId });
const confirm = (state: State, receipt?: ManagementReceipt, submissionId = "submit") => reducer(state, { type: "management_confirmed", submissionId, receipt });
const running = () => reducer(initialState, { type: "runtime_snapshot", snapshot: runtime(1, operation) });
const terminal = { ...operation, status: "completed", activity: "finalizing", operationRevision: 3, applied: true, summary: "durable summary" };
const busyReceipt = { operationId: operation.operationId, errorCode: "maintenance_busy" };

test("management refusal retains operation identity through the submit adapter; legacy success and transport errors keep their meaning", async () => {
  for (const managementErrorCode of [undefined, "maintenance_busy", "maintenance_recovery_required", "future_code"]) {
    const bridge = { StartTurnForTab: async () => ({ disposition: "management_handled", operationId: operation.operationId, managementErrorCode }) } as unknown as AppBindings;
    assert.deepEqual(await submitTurn(bridge, "A", "submit", "/compact", "/compact", ""), [2, { operationId: operation.operationId, errorCode: managementErrorCode }]);
  }
  const bridge = { StartTurnForTab: async () => { throw new Error("connection closed"); } } as unknown as AppBindings;
  await assert.rejects(submitTurn(bridge, "A", "submit", "/compact", "/compact", ""), /connection closed/);
});

test("duplicate compact has no chat echo, preserves runtime-owned Stop state and deduplicates the busy notice", () => {
  let state = confirm(running(), busyReceipt);
  assert.deepEqual(state.localSubmissionOrder, []);
  assert.equal(state.running, false, "management does not fabricate a conversational turn");
  assert.equal(state.turnActive, false);
  assert.equal(state.runtimeStateSnapshot?.cancellable, true);
  assert.equal(state.items.filter(item => item.kind === "compaction").length, 1);
  assert.equal(state.items.some(item => item.kind === "notice" && item.level === "warn"), false);
  state = confirm(state, busyReceipt);
  assert.equal(state.items.filter(item => item.kind === "notice").length, 1);
});

test("a late management receipt cannot clear a newer submission or reopen completed compaction", () => {
  let state = running();
  state = reducer(state, { type: "event", e: { kind: "session_operation", sessionOperation: terminal } });
  state = reducer(state, { type: "runtime_snapshot", snapshot: runtime(3, undefined) });
  state = confirm(state, busyReceipt);
  assert.equal(state.running, false);
  assert.equal(state.items.some(item => item.kind === "notice"), false);
  assert.equal(state.items.find(item => item.kind === "compaction")?.status, "completed");
  state = pending(pending(running()), "newer");
  const later = confirm(state, busyReceipt);
  assert.equal(later.pendingSubmissionId, "newer");
  assert.equal(later.localSubmissions.submit, undefined);
  assert.equal(later.localSubmissions.newer.status, "sending");
});

test("recovery and unknown command refusals remain visible without a failed conversational bubble", () => {
  for (const errorCode of ["maintenance_recovery_required", "future_code"]) {
    const state = confirm(running(), { operationId: operation.operationId, errorCode });
    assert.deepEqual(state.localSubmissionOrder, []);
    assert.equal(state.items.filter(item => item.kind === "notice" && item.level === "warn").length, 1);
  }
});

test("switching away during compaction and restoring durable history rebuilds one completed summary", () => {
  const background = confirm(running(), busyReceipt);
  const other = reducer(initialState, { type: "history", messages: [{ role: "user", content: "unrelated session", messageId: "B" }] });
  const completed = reducer(background, { type: "event", e: { kind: "session_operation", sessionOperation: terminal } });
  const body = { role: "compaction", id: `maintenance:${operation.operationId}`, content: JSON.stringify(terminal) };
  const message = canonicalMessage({ messageId: body.id, role: body.role, position: 2, eventSequence: 3, version: 1, visibleTurn: 1 }, body);
  const snapshot: TranscriptSnapshot = { protocolVersion: 1, snapshotId: "completed-A", identity: {
    sessionId: "A", runtimeEpoch: "epoch-A", rewriteEpoch: 0, headId: "" }, projectionRevision: 3,
    coveredThroughSeq: 3, records: [{ id: body.id, order: 0, message, refs: [] }], activeRecords: [], activeAttempts: [],
    runtime: { status: "completed", pendingEvents: [] }, before: 0, hasOlder: false, totalRecords: 1, totalTurns: 1, stale: false };
  const projection = { items: historyMessagesToItems([message], "snapshot:").items, removeIds: [], startTurn: 0, endTurn: 1,
    totalTurns: 1, hasOlder: false, hasNewer: false, revision: 3, revisionKnown: true, digest: "completed-A" };
  for (const state of [completed, reducer(completed, { type: "reset" })]) {
    let restored = reducer(state, { type: "transcript_v2_snapshot", snapshot, projection });
    restored = reducer(restored, { type: "runtime_snapshot", snapshot: runtime(3, undefined) });
    const cards = restored.items.filter(item => item.kind === "compaction");
    assert.equal(cards.length, 1);
    assert.equal(cards[0].status, "completed");
    assert.equal(cards[0].summary, terminal.summary);
    assert.equal(cards[0].pending, false);
    assert.deepEqual(restored.localSubmissionOrder, []);
  }
  assert.equal(other.items.some(item => item.kind === "compaction"), false);
});

test("only compact and its focus form use the management route", () => {
  for (const text of ["/compact", " /compact keep decisions "]) assert.equal(isCompactCommand(text), true);
  for (const text of ["/compactly", "continue", "explain /compact", "/context"]) assert.equal(isCompactCommand(text), false);
});

test("expanded and edited compact focus uses the typed command route without rewriting instructions", async () => {
  const calls: unknown[][] = [];
  const bridge = {
    StartTurnForTab: async (...args: unknown[]) => { calls.push(args); return { disposition: "management_handled", operationId: operation.operationId, managementErrorCode: "maintenance_busy" }; },
    SubmitDisplayToTabWithID: async () => { throw Error("display route bypassed management admission"); },
    SubmitEditedDisplayToTabWithID: async () => { throw Error("edit route bypassed management admission"); },
  } as unknown as AppBindings;
  const focus = `/compact preserve decisions\n${"source details ".repeat(200)}`;
  for (const original of ["", "previous draft"]) {
    assert.equal(isCompactSubmission(focus), true);
    assert.deepEqual(await submitTurn(bridge, "A", "request", "/compact [Pasted text #1]", focus, original), [2, busyReceipt]);
  }
  assert.deepEqual(calls, [["A", focus, "request"], ["A", focus, "request"]]);
  assert.equal(isCompactSubmission(focus, { display: focus, input: focus, invocations: [] }), false);
  assert.equal(isCompactSubmission(focus, undefined, { goal: focus }), false);
});

test("every receipt/completion/idle ordering settles without pinning a phantom turn", () => {
  const permutations = [["receipt", "terminal", "idle"], ["receipt", "idle", "terminal"],
    ["terminal", "receipt", "idle"], ["terminal", "idle", "receipt"],
    ["idle", "receipt", "terminal"], ["idle", "terminal", "receipt"]];
  for (const order of permutations) {
    let state: State = { ...running(), transcriptProtocol: 2 };
    for (const step of order) {
      if (step === "receipt") state = confirm(state, busyReceipt);
      if (step === "terminal") {
        state = reducer(state, { type: "event", e: { kind: "session_operation", sessionOperation: terminal } });
        state = reducer(state, { type: "transcript_runtime", runtime: { status: "completed", turnId: "previous-turn", submissionId: "previous-submission", pendingEvents: [], samplingCount: 0, toolCount: 0 } });
      }
      if (step === "idle") state = reducer(state, { type: "runtime_snapshot", snapshot: runtime(3, undefined) });
    }
    assert.equal(state.running, false, order.join(" -> "));
    assert.equal(state.cancellable, false);
    assert.equal(state.pendingSubmissionId, undefined);
    assert.equal(state.runtimeStateSnapshot?.running, false);
    assert.equal(state.items.find(item => item.kind === "compaction")?.status, "completed");
    const store = new TranscriptStore({} as TranscriptBackend);
    store.setPinned("A", Boolean(state.running || state.turnActive || state.live));
    assert.equal(store.tabIsPinned("A"), false);
  }
});

test("a late compact receipt preserves a newer active chat and pending prompt", () => {
  const state: State = { ...pending(initialState, "new-chat"), running: true, turnActive: true,
    pendingPrompt: true, activeTurnId: "new-turn", cancellable: true, cancelRequested: true };
  const next = confirm(state, busyReceipt, "old-compact");
  for (const key of ["running", "turnActive", "pendingPrompt", "activeTurnId", "cancellable", "cancelRequested", "pendingSubmissionId", "localSubmissions"] as const) {
    assert.equal(next[key], state[key], key);
  }
});

test("management requests reserve distinct submission identities without starting chat lifecycle", () => {
  let state = initialState;
  const ids: string[] = [];
  for (let i = 0; i < 3; i++) {
    ids.push(createTurnSubmissionId("A", state.sessionGen, state.seq));
    state = reducer(state, { type: "management_requested" });
    assert.equal(state.running, false);
    assert.deepEqual(state.localSubmissionOrder, []);
  }
  const chatId = createTurnSubmissionId("A", state.sessionGen, state.seq);
  assert.equal(new Set([...ids, chatId]).size, 4);
  state = pending(state, chatId);
  for (const id of ids) state = confirm(state, busyReceipt, id);
  assert.equal(state.pendingSubmissionId, chatId);
  assert.equal(state.running, true);
});

test("legacy compact bindings and conversational display submissions retain their contracts", async () => {
  const calls: unknown[][] = [];
  const bridge = {
    SubmitToTabWithID: async (...args: unknown[]) => { calls.push(["legacy", ...args]); },
    SubmitDisplayToTabWithID: async (...args: unknown[]) => { calls.push(["display", ...args]); },
    SubmitInvocationsToTabWithID: async (...args: unknown[]) => { calls.push(["structured", ...args]); },
    SubmitInitialGoalToTabWithID: async (...args: unknown[]) => { calls.push(["goal", ...args]); return ["approval"]; },
  } as unknown as AppBindings;
  assert.deepEqual(await submitTurn(bridge, "A", "legacy", "/compact [paste]", "/compact decisions", ""), [0]);
  await submitTurn(bridge, "A", "chat", "visible", "hidden context\nvisible", "");
  await submitTurn(bridge, "A", "structured", "/compact", "/compact", "", { display: "tool task", input: "tool input", invocations: [] });
  assert.deepEqual(await submitTurn(bridge, "A", "goal", "task", "/compact", "", undefined,
    { goal: "task", collaborationMode: "goal", toolApprovalMode: "ask" }), [1, ["approval"]]);
  assert.deepEqual(calls.map(call => call[0]), ["legacy", "display", "structured", "goal"]);
  assert.deepEqual(calls[0], ["legacy", "A", "/compact decisions", "legacy"]);
  assert.deepEqual(calls[1], ["display", "A", "visible", "hidden context\nvisible", "chat"]);
});
