import assert from "node:assert/strict";
import { canonicalMessage } from "../lib/canonicalTranscriptBackend";
import type { RuntimeState } from "../lib/runtimeStateStore";
import { applyResolvedField, entryToRecord } from "../lib/transcriptRecordProjection";
import type { HistoryContentRef } from "../lib/types";
import { initialState, reducer } from "../lib/useController";
import type { Item, State } from "../lib/useController";

type CompactionItem = Extract<Item, { kind: "compaction" }>;

function operationCard(state: State, operationId: string): CompactionItem | undefined {
  return state.items.find((item): item is CompactionItem => item.kind === "compaction" && item.operationId === operationId);
}

const runtime = (revision: number, maintenance: RuntimeState["maintenance"]): RuntimeState => ({
  schemaVersion: 1,
  projectionEpoch: "projection-1",
  runtimeEpoch: "runtime-1",
  activityRevision: revision,
  revision,
  phase: maintenance?.activity === "recovery_required" ? "recovery_required" : "executing",
  running: Boolean(maintenance),
  turnId: "",
  turnStatus: "",
  turnEventSeq: 0,
  pendingPrompt: false,
  cancelRequested: maintenance?.activity === "cancelling",
  cancellable: Boolean(maintenance),
  backgroundJobs: 0,
  activity: maintenance ? "maintenance" : "idle",
  maintenance,
});

let state = reducer(initialState, { type: "event", e: {
  kind: "session_operation",
  sessionOperation: { operationId: "op-1", kind: "compact", activity: "running", status: "running", inputTokens: 1200, operationRevision: 1, runtimeEpoch: "runtime-1" },
} });
let cards = state.items.filter(item => item.kind === "compaction");
assert.equal(cards.length, 1);
assert.equal(cards[0].pending, true);
assert.equal(cards[0].id, "maintenance:op-1");

state = reducer(state, { type: "event", e: {
  kind: "session_operation",
  sessionOperation: { operationId: "op-1", kind: "compact", activity: "cancelling", status: "cancelling", inputTokens: 1200, operationRevision: 2, runtimeEpoch: "runtime-1" },
} });
cards = state.items.filter(item => item.kind === "compaction");
assert.equal(cards.length, 1, "cancellation updates the stable operation card");
assert.equal(cards[0].status, "cancelling");

state = reducer(state, { type: "event", e: {
  kind: "compaction_done",
  compaction: { trigger: "manual", messages: 8, summary: "summary", archive: "archive.jsonl" },
} });
cards = state.items.filter(item => item.kind === "compaction");
assert.equal(cards.length, 1, "agent completion metadata does not create a second manual card");
assert.equal(cards[0].summary, "summary");

state = reducer(state, { type: "event", e: {
  kind: "session_operation",
  sessionOperation: { operationId: "op-1", kind: "compact", activity: "finalizing", status: "completed", applied: true, inputTokens: 1200, resultTokens: 400, messages: 8, summary: "summary", operationRevision: 4, runtimeEpoch: "runtime-1" },
} });
cards = state.items.filter(item => item.kind === "compaction");
assert.equal(cards.length, 1);
assert.equal(cards[0].pending, false);
assert.equal(cards[0].status, "completed");
assert.equal(cards[0].resultTokens, 400);

// An event delayed behind terminal persistence cannot regress the operation.
state = reducer(state, { type: "event", e: {
  kind: "session_operation",
  sessionOperation: { operationId: "op-1", kind: "compact", activity: "cancelling", status: "cancelling", operationRevision: 3, runtimeEpoch: "runtime-1" },
} });
cards = state.items.filter(item => item.kind === "compaction");
assert.equal(cards[0].status, "completed");
assert.equal(cards[0].pending, false);
assert.equal(cards[0].operationRevision, 4);

// A runtime refresh is a partial observation: omitted result fields retain the
// durable event values and a terminal card does not become pending again.
state = reducer(state, { type: "runtime_snapshot", snapshot: runtime(5, {
  operationId: "op-1",
  kind: "compact",
  activity: "running",
  operationRevision: 3,
  runtimeEpoch: "runtime-1",
}) });
cards = state.items.filter(item => item.kind === "compaction");
assert.equal(cards[0].status, "completed");
assert.equal(cards[0].applied, true);
assert.equal(cards[0].inputTokens, 1200);
assert.equal(cards[0].resultTokens, 400);

let failed = reducer(initialState, { type: "event", e: {
  kind: "session_operation",
  sessionOperation: {
    operationId: "op-failed", kind: "compact", activity: "recovery_required", status: "recovery_required",
    errorCode: "save_failed", detail: "disk full", applied: true, inputTokens: 900, resultTokens: 350,
    operationRevision: 7, runtimeEpoch: "runtime-1",
  },
} });
failed = reducer(failed, { type: "runtime_snapshot", snapshot: runtime(8, {
  operationId: "op-failed",
  kind: "compact",
  activity: "recovery_required",
  operationRevision: 7,
  runtimeEpoch: "runtime-1",
}) });
const failedCard = failed.items.filter(item => item.kind === "compaction").find(item => item.operationId === "op-failed");
assert.equal(failedCard?.detail, "disk full");
assert.equal(failedCard?.errorCode, "save_failed");
assert.equal(failedCard?.applied, true);
assert.equal(failedCard?.inputTokens, 900);
assert.equal(failedCard?.resultTokens, 350);

// session-maintenance-v1 stores a provider compaction row whose content is the
// operation JSON. Canonical history must recover the display fields, not show
// the JSON as a successful generic compaction.
const persisted = canonicalMessage({
  messageId: "maintenance:op-history", position: 4, version: 1, role: "compaction", eventSequence: 9, visibleTurn: 2,
}, {
  id: "maintenance:op-history",
  role: "compaction",
  content: JSON.stringify({
    operationId: "op-history", kind: "compact", activity: "recovery_required", status: "recovery_required",
    errorCode: "save_failed", detail: "could not save", applied: true, inputTokens: 1000, resultTokens: 450,
    operationRevision: 6, runtimeEpoch: "runtime-history",
  }),
});
assert.equal(persisted.role, "compaction");
assert.equal(persisted.operationId, "op-history");
assert.equal(persisted.operationStatus, "recovery_required");
assert.equal(persisted.errorCode, "save_failed");
assert.equal(persisted.detail, "could not save");
assert.equal(persisted.applied, true);
assert.equal(persisted.operationRevision, 6);
assert.equal(persisted.runtimeEpoch, "runtime-history");

// Large summaries are reference-backed. Their placeholder must be loading (or
// explicitly unavailable), never a false success, and hydration uses the same
// canonical parser as the inline row.
const lazySource = {
  id: "maintenance:op-lazy",
  role: "compaction",
  content: JSON.stringify({
    operationId: "op-lazy", kind: "compact", activity: "finalizing", status: "failed",
    errorCode: "summary_failed", detail: "late body", summary: "x".repeat(5000),
    operationRevision: 9, runtimeEpoch: "runtime-lazy",
  }),
};
const lazyPersistent = {
  messageId: "maintenance:op-lazy", position: 6, version: 1, role: "compaction", eventSequence: 11, visibleTurn: 2,
  contentRef: { digest: "lazy", bytes: JSON.stringify(lazySource).length },
};
const lazyMessage = canonicalMessage(lazyPersistent, undefined);
assert.equal(lazyMessage.operationStatus, "loading");
assert.equal(lazyMessage.pending, true);
const lazyRef: HistoryContentRef = { entryId: "m:maintenance:op-lazy", field: "canonicalMessage", size: lazyPersistent.contentRef.bytes, chunks: 1, revision: 1, digest: "lazy" };
const lazyRecord = entryToRecord({ entryId: lazyRef.entryId, turn: 2, order: 6, message: lazyMessage, refs: [lazyRef] });
const lazyBytes = [...new TextEncoder().encode(JSON.stringify(lazySource))].map(byte => String.fromCharCode(byte)).join("");
assert.equal(applyResolvedField(lazyRecord, lazyRef, lazyBytes), true);
assert.equal(lazyRecord.message.operationStatus, "failed");
assert.equal(lazyRecord.message.errorCode, "summary_failed");
assert.equal(lazyRecord.message.detail, "late body");
assert.equal(lazyRecord.message.operationRevision, 9);

// History and live state use the operation id as one stable node identity.
let reconciled = reducer(initialState, { type: "history", messages: [persisted] });
reconciled = reducer(reconciled, { type: "runtime_snapshot", snapshot: runtime(1, {
  operationId: "op-history", kind: "compact", activity: "recovery_required",
  operationRevision: 6, runtimeEpoch: "runtime-history", status: "recovery_required",
}) });
const reconciledCards = reconciled.items.filter(item => item.kind === "compaction").filter(item => item.operationId === "op-history");
assert.equal(reconciledCards.length, 1);
assert.equal(reconciledCards[0].id, "maintenance:op-history");
assert.equal(reconciledCards[0].errorCode, "save_failed");

// The inverse arrival order is equally monotonic: an in-flight history read
// cannot replace a newer live terminal record with its older running row.
const staleHistory = canonicalMessage({
  messageId: "maintenance:op-race", position: 5, version: 1, role: "compaction", eventSequence: 10, visibleTurn: 2,
}, {
  id: "maintenance:op-race",
  role: "compaction",
  content: JSON.stringify({
    operationId: "op-race", kind: "compact", activity: "running", status: "running",
    inputTokens: 800, operationRevision: 1, runtimeEpoch: "runtime-1",
  }),
});
let liveFirst = reducer(initialState, { type: "event", e: {
  kind: "session_operation",
  sessionOperation: {
    operationId: "op-race", kind: "compact", activity: "finalizing", status: "failed",
    errorCode: "summary_failed", detail: "provider failed", inputTokens: 800, resultTokens: 800,
    operationRevision: 3, runtimeEpoch: "runtime-1",
  },
} });
liveFirst = reducer(liveFirst, { type: "history", messages: [staleHistory] });
const liveFirstCards = liveFirst.items.filter(item => item.kind === "compaction").filter(item => item.operationId === "op-race");
assert.equal(liveFirstCards.length, 1);
assert.equal(liveFirstCards[0].status, "failed");
assert.equal(liveFirstCards[0].detail, "provider failed");
assert.equal(liveFirstCards[0].operationRevision, 3);

// A persisted progress row stays pending until runtime synchronization can
// prove whether its operation still exists. Both arrival orders converge on
// interrupted when the synchronized runtime is idle.
let historyBeforeRuntime = reducer(initialState, { type: "history", messages: [staleHistory] });
let historyBeforeRuntimeCard = operationCard(historyBeforeRuntime, "op-race");
assert.equal(historyBeforeRuntimeCard?.pending, true);
assert.equal(historyBeforeRuntimeCard?.status, "confirming");
historyBeforeRuntime = reducer(historyBeforeRuntime, { type: "runtime_snapshot", snapshot: runtime(11, undefined) });
historyBeforeRuntimeCard = operationCard(historyBeforeRuntime, "op-race");
assert.equal(historyBeforeRuntimeCard?.pending, false);
assert.equal(historyBeforeRuntimeCard?.status, "interrupted");

let runtimeBeforeHistory = reducer(initialState, { type: "runtime_snapshot", snapshot: runtime(12, undefined) });
runtimeBeforeHistory = reducer(runtimeBeforeHistory, { type: "history", messages: [staleHistory] });
const runtimeBeforeHistoryCards = runtimeBeforeHistory.items.filter((item): item is CompactionItem => item.kind === "compaction" && item.operationId === "op-race");
assert.equal(runtimeBeforeHistoryCards.length, 1);
assert.equal(runtimeBeforeHistoryCards[0].pending, false);
assert.equal(runtimeBeforeHistoryCards[0].status, "interrupted");

// A matching active identity retains its running state after synchronization.
let activeRecovery = reducer(initialState, { type: "history", messages: [staleHistory] });
activeRecovery = reducer(activeRecovery, { type: "runtime_snapshot", snapshot: runtime(13, {
  operationId: "op-race", kind: "compact", activity: "running", status: "running",
  operationRevision: 1, runtimeEpoch: "runtime-1",
}) });
const activeRecoveryCards = activeRecovery.items.filter((item): item is CompactionItem => item.kind === "compaction" && item.operationId === "op-race");
assert.equal(activeRecoveryCards.length, 1);
assert.equal(activeRecoveryCards[0].pending, true);
assert.equal(activeRecoveryCards[0].status, "running");

console.log("maintenance lifecycle: persisted parsing, monotonic merge and reconciliation passed");

// A previously cached idle snapshot is not evidence that a newer live
// operation has stopped. History refresh must not manufacture a terminal.
let cachedIdle = reducer(initialState, { type: "runtime_snapshot", snapshot: runtime(20, undefined) });
const freshOp = { operationId: "op-race", kind: "compact", activity: "running", status: "running", operationRevision: 1, runtimeEpoch: "runtime-1" };
cachedIdle = reducer(cachedIdle, { type: "event", e: { kind: "session_operation", sessionOperation: freshOp } });
cachedIdle = reducer(cachedIdle, { type: "history", messages: [staleHistory] });
assert.equal(operationCard(cachedIdle, "op-race")?.status, "running");
cachedIdle = reducer(cachedIdle, { type: "runtime_snapshot", snapshot: runtime(21, freshOp) });
assert.equal(operationCard(cachedIdle, "op-race")?.status, "running");

// An inferred interruption is reversible when fresh authoritative evidence
// arrives; a persisted interruption remains a real terminal state.
historyBeforeRuntime = reducer(historyBeforeRuntime, { type: "runtime_snapshot", snapshot: runtime(22, freshOp) });
assert.equal(operationCard(historyBeforeRuntime, "op-race")?.status, "running");

for (const status of ["future_state", ""]) {
  const unknown = reducer(initialState, { type: "event", e: { kind: "session_operation", sessionOperation: { ...freshOp, status, activity: "" } } });
  assert.equal(operationCard(unknown, "op-race")?.status, "unavailable");
  assert.equal(operationCard(unknown, "op-race")?.pending, false);
}

let formalInterruption = reducer(initialState, { type: "event", e: { kind: "session_operation", sessionOperation: { ...freshOp, status: "interrupted", operationRevision: 2 } } });
formalInterruption = reducer(formalInterruption, { type: "runtime_snapshot", snapshot: runtime(23, { ...freshOp, operationRevision: 3 }) });
assert.equal(operationCard(formalInterruption, "op-race")?.status, "interrupted", "formal terminal states remain irreversible");
const unknownSaving = reducer(initialState, { type: "event", e: { kind: "session_operation", sessionOperation: { ...freshOp, status: "future_state", activity: "finalizing" } } });
assert.equal(operationCard(unknownSaving, "op-race")?.status, "unavailable", "unknown status cannot inherit a familiar activity");
