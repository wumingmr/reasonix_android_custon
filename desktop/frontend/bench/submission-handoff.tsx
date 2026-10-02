import { useSyncExternalStore } from "react";
import { createRoot } from "react-dom/client";
import { Transcript } from "../src/components/Transcript";
import { Composer } from "../src/components/Composer";
import { LocaleProvider } from "../src/lib/i18n";
import { ToastProvider } from "../src/lib/toast";
import { initialState, reducer, createTurnSubmissionId, type Action } from "../src/lib/useController";
import { orderedLocalSubmissions } from "../src/lib/localSubmissionState";
import { getTranscriptStore } from "../src/lib/transcriptStore";
import { TranscriptSessionFollower } from "../src/lib/transcriptSessionFollower";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import { setFrontendDiagnosticSink } from "../src/lib/frontendDiagnosticBridge";
import type { FollowRequest, HistoryWindowRequest, Message, TranscriptFollowResponse } from "../src/generated/desktopContract.generated";
import "../src/styles.css";

const tab = "handoff-integration", path = "/disposable/handoff.jsonl", store = getTranscriptStore();
let state = { ...initialState }, revision = 1, coverage = 1;
const database: Message[] = Array.from({ length: 1000 }, (_, index) => [
  { role: "user", messageId: `user-${index}`, turnId: `turn-${index}`, content: `Question ${index}`, historyTurn: index + 1 },
  { role: "assistant", messageId: `answer-${index}`, turnId: `turn-${index}`, content: `Answer ${index}. Stable reading paragraph.\n\nSecond paragraph.`, historyTurn: index + 1, turnFinal: true },
]).flat();
let polls: Array<(response: TranscriptFollowResponse) => void> = [];
let queued: NonNullable<TranscriptFollowResponse["changes"]> = [];
let pending: { id: string; text: string; messageId: string; turnId: string } | undefined;
let presentation: Record<string, unknown> = {};
setFrontendDiagnosticSink((source, type, fields) => { if (source === "transcript" && type === "presentation") presentation = fields; });
const dispatch = (action: Action) => { state = reducer(state, action); store.setState(tab, state); };
function windowPage(request: HistoryWindowRequest) {
  const limit = request.limit ?? 32;
  let start = request.anchor === "newest" ? Math.max(0, database.length - limit)
    : request.anchor === "message" ? Math.max(0, database.findIndex(item => item.messageId === request.messageId))
    : Number(request.cursor);
  if (request.anchor === "cursor" && request.direction === "older") start = Math.max(0, start - limit);
  const end = Math.min(database.length, start + limit);
  return { status: "ready", generation: "fixture", snapshotSequence: coverage, coverageSequence: coverage,
    totalTurns: 1000 + (database.length > 2000 ? 1 : 0), hasOlder: start > 0, hasNewer: end < database.length,
    olderCursor: start > 0 ? String(start) : "", newerCursor: end < database.length ? String(end) : "",
    messages: database.slice(start, end).map((message, offset) => ({ messageId: message.messageId!, role: message.role,
      position: start + offset, version: 1, eventSequence: coverage, visibleTurn: message.historyTurn, submissionId: message.submissionId, inline: message })) };
}
function publish(change: Omit<NonNullable<TranscriptFollowResponse["changes"]>[number], "revision" | "index" | "durableSeq" | "commitSeq">, commit = false) {
  if (commit) coverage++;
  queued.push({ ...change, revision: ++revision, index: 0, durableSeq: coverage, commitSeq: coverage, ...(commit ? { firstSeq: coverage } : {}) });
  flush();
}
function flush() {
  if (!polls.length || !queued.length) return;
  polls.shift()!({ protocolVersion: 2, subscription: tab, changes: queued.splice(0), resetRequired: false });
}
installDesktopHostStub({
  Commands: () => [],
  ModelsForTab: () => [],
  Models: () => [],
  TranscriptFollowForTab: (_tab: string, request: FollowRequest) => {
    if (request.close) return { protocolVersion: 2, subscription: tab, changes: [], resetRequired: false };
    if (!request.subscription) return { protocolVersion: 2, subscription: tab, changes: [], resetRequired: false, history: windowPage({ anchor: "newest" }), snapshot: {
      protocolVersion: 1, snapshotId: "cut", identity: { sessionId: tab, runtimeEpoch: "fixture", rewriteEpoch: 0, headId: "" },
      projectionRevision: revision, coveredThroughSeq: coverage, durableSeq: coverage, records: [], activeRecords: [], activeAttempts: [],
      runtime: { status: "completed", pendingEvents: [], samplingCount: 0, toolCount: 0 }, before: 0, hasOlder: true, totalRecords: database.length, totalTurns: 1000, stale: false,
    } };
    return new Promise<TranscriptFollowResponse>(resolve => { polls.push(resolve); flush(); });
  },
  SessionHistoryWindowForTab: (_tab: string, request: HistoryWindowRequest) => windowPage(request),
  TranscriptOutlineForTab: () => ({ turns: [], hasMore: false }),
});
const follower = new TranscriptSessionFollower(tab, path, false, dispatch, () => state);
await follower.start();

async function page(direction: "older" | "newer" | "latest") {
  const loaded = direction === "older" ? await store.loadOlder(tab, path)
    : direction === "newer" ? await store.loadNewer(tab, path) : await store.loadLatest(tab, path);
  if (!loaded) return;
  dispatch({ type: "history_replace", ...loaded });
}
function send(text: string) {
  const id = createTurnSubmissionId(tab, state.sessionGen, state.seq);
  pending = { id, text, messageId: `sent-${state.seq}`, turnId: `sent-turn-${state.seq}` };
  dispatch({ type: "user", text, submissionId: id, seq: state.seq });
}
function event() {
  if (!pending) return;
  publish({ event: { kind: "user_message", source: "executor", messageId: pending.messageId, submissionId: pending.id, turnId: pending.turnId } });
}
function record(withSubmission = true) {
  if (!pending) return;
  const message: Message = { role: "user", messageId: pending.messageId, turnId: pending.turnId, submissionId: withSubmission ? pending.id : undefined, content: pending.text, historyTurn: 1001 };
  if (!database.some(item => item.messageId === message.messageId)) database.push(message);
  publish({ records: [message] }, true);
}
declare global { interface Window { handoff: { page: typeof page; event: typeof event; record: typeof record; batched(): void; send: typeof send; inspect(): unknown; finish(): void; output(): void } } }
window.handoff = { page, event, record, send,
  batched() { event(); record(false); },
  output() { if (pending) publish({ event: { kind: "text", messageId: `output-${pending.messageId}`, turnId: pending.turnId, text: "Live answer with stable text." } }); },
  finish() {
    if (pending) {
      dispatch({ type: "send_confirmed", submissionId: pending.id });
      publish({ event: { kind: "message", messageId: `output-${pending.messageId}`, turnId: pending.turnId,
        text: "Live answer with stable text.", reasoning: "Deterministic process details." } });
    }
    publish({ runtime: { status: "completed", submissionId: pending?.id, pendingEvents: [], samplingCount: 0, toolCount: 0 } });
  },
  inspect() { return { ids: state.items.map(item => item.id), locals: state.localSubmissionOrder.length, handoffs: Object.keys(state.visibleSubmissionHandoffs).length,
    users: state.items.filter(item => item.kind === "user").length, stats: store.stats(), presentation, pending, hasNewer: state.historyHasNewer, revision: state.localSubmissionSendRevision }; },
};
function Fixture() {
  const current = useSyncExternalStore(listener => store.subscribeState(tab, listener), () => store.states.get(tab)!);
  return <div style={{ height: "100vh", display: "flex", flexDirection: "column" }}>
    <div><button onClick={() => void page("older")}>Older</button><button onClick={() => void page("newer")}>Newer</button><button onClick={() => void page("latest")}>Latest</button>
      <button onClick={() => pending && dispatch({ type: "send_confirmed", submissionId: pending.id })}>Accept</button>
      <button onClick={event}>Identity</button><button onClick={() => record()}>Commit</button><button onClick={() => window.handoff.batched()}>Batched</button></div>
    <Transcript items={current.items} tabId={tab} running={current.running} localSubmissions={orderedLocalSubmissions(current)}
      visibleSubmissionHandoffs={current.visibleSubmissionHandoffs} localSubmissionSendRevision={current.localSubmissionSendRevision}
      hasOlderHistory={current.historyHasOlder} hasNewerHistory={current.historyHasNewer} onPrompt={() => {}} onFork={() => {}} />
    <Composer running={current.running} collaborationMode="normal" toolApprovalMode="ask" modelLabel="Fixture" tabId={tab}
      onSend={send} onCancel={async () => ({ discardedItemIds: [] })} onCycleMode={() => {}} onSetMode={() => {}} onSetCollaborationMode={() => {}}
      onSetToolApprovalMode={() => {}} onClearGoal={() => {}} onPauseGoal={() => {}} onResumeGoal={() => {}}
      onEditGoal={() => {}}
      onSwitchModel={() => true} onSetEffort={() => {}} />
  </div>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><ToastProvider><Fixture /></ToastProvider></LocaleProvider>);
