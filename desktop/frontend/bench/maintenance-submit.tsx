import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import { Composer } from "../src/components/Composer";
import { Transcript } from "../src/components/Transcript";
import { LocaleProvider } from "../src/lib/i18n";
import { ToastProvider } from "../src/lib/toast";
import { initialState, reducer, type State } from "../src/lib/useController";
import { runtimeStateStore, type RuntimeState } from "../src/lib/runtimeStateStore";
import { orderedLocalSubmissions } from "../src/lib/localSubmissionState";
import { sessionOperationHistoryMessage } from "../src/lib/sessionMaintenanceOperation";
import { submitTurn } from "../src/lib/turnSubmit";
import type { AppBindings } from "../src/lib/bridge";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import "../src/styles.css";

const root = createRoot(document.getElementById("root")!);
const operation = { operationId: "compact-A", kind: "compact", activity: "running", status: "running", operationRevision: 1, runtimeEpoch: "epoch-A" };
const terminal = { ...operation, status: "completed", activity: "finalizing", operationRevision: 3, summary: "The durable compaction summary survived switching sessions.", applied: true };
const runtime = (revision: number, maintenance?: RuntimeState["maintenance"]): RuntimeState => ({
  schemaVersion: 1, projectionEpoch: "projection-A", runtimeEpoch: "epoch-A", activityRevision: revision, revision,
  phase: maintenance ? "executing" : "idle", running: Boolean(maintenance), turnId: "", turnStatus: "", turnEventSeq: 0,
  pendingPrompt: false, cancelRequested: false, cancellable: Boolean(maintenance), backgroundJobs: 0, activity: maintenance ? "maintenance" : "", maintenance,
});
const states = new Map<string, State>(["A", "B"].map(id => [id, reducer(initialState, { type: "history", messages: [{ role: "user", messageId: id, content: `Session ${id}` }] })]));
let active = "A";
let submissions = 0;
let starts = 0;
let lastInput = "";
let revision = 0;
let admission: Promise<void> | undefined;
let releaseAdmission: (() => void) | undefined;
let rejectAdmission: ((reason: Error) => void) | undefined;
const noop = () => {};
// The Composer captures the real submission target before asynchronous preparation.
installDesktopHostStub({
  CaptureInboxTarget: (tabId: string, sessionPath: string) => ({ tabId, sessionPath, generation: 1, selection: 0, remote: false }),
  Commands: () => [], Models: () => [], ModelsForTab: () => [],
  TranscriptOutlineForTab: () => ({ turns: [], hasMore: false }),
});
const bridge = { StartTurnForTab: async (tab: string, input: string) => {
  lastInput = input;
  await admission;
  if (states.get(tab)!.runtimeStateSnapshot?.maintenance) return { disposition: "management_handled", operationId: operation.operationId, managementErrorCode: "maintenance_busy" };
  starts++;
  states.set(tab, reducer(states.get(tab)!, { type: "runtime_snapshot", snapshot: runtime(1, operation) }));
  paint();
  return { disposition: "management_handled", operationId: operation.operationId };
} } as unknown as AppBindings;
async function send(display: string, input = display, tab = active) {
  const submissionId = `submit-${++submissions}`;
  states.set(tab, reducer(states.get(tab)!, { type: "management_requested" }));
  const [outcome, receipt] = await submitTurn(bridge, tab, submissionId, display, input, "");
  if (outcome !== 2) throw new Error("expected management receipt");
  states.set(tab, reducer(states.get(tab)!, { type: "management_confirmed", submissionId, receipt }));
  paint();
}
function paint() {
  runtimeStateStore.commit({ epoch: "fixture", revision: ++revision, topics: [], sessions: [...states.entries()].map(([tabId, state]) => ({
    tabId, scope: "global", workspaceRoot: "", topicId: "", sessionPath: `fixture-${tabId}`, sessionGeneration: 1,
    open: true, remote: false, freshness: "synced", state: state.runtimeStateSnapshot ?? runtime(0),
  })) });
  const state = states.get(active)!;
  flushSync(() => root.render(<LocaleProvider><ToastProvider>
    <div style={{ height: "100vh", display: "flex", flexDirection: "column" }}>
      <nav><button onClick={() => { active = active === "A" ? "B" : "A"; paint(); }}>Switch session</button><span data-active-session>{active}</span></nav>
      <div style={{ flex: 1, minHeight: 0, position: "relative" }}><Transcript items={state.items} tabId={active} geometrySessionKey={active}
        localSubmissions={orderedLocalSubmissions(state)} running={state.running} onPrompt={noop} /></div>
      <div style={{ width: "100%", padding: "16px 24px", boxSizing: "border-box" }}><Composer tabId={active} sessionKey={active} inboxSessionPath={`fixture-${active}`} running={state.running} commandCatalog={[]}
        ready collaborationMode="normal" toolApprovalMode="ask" modelLabel="Fixture" onSend={send}
        onCancel={async () => ({ discardedItemIds: [] })} onCycleMode={noop} onSetMode={noop} onSetCollaborationMode={noop}
        onSetToolApprovalMode={noop} onClearGoal={noop} onEditGoal={noop} onPauseGoal={noop} onResumeGoal={noop}
        onSwitchModel={() => true} onSetEffort={noop} /></div>
    </div>
  </ToastProvider></LocaleProvider>));
}
Object.assign(window, { maintenanceProbe: {
  holdAdmission: () => { admission = new Promise<void>((resolve, reject) => { releaseAdmission = resolve; rejectAdmission = reject; }); },
  releaseAdmission: () => { releaseAdmission?.(); admission = undefined; },
  rejectAdmission: () => { rejectAdmission?.(new Error("fixture admission failure")); admission = undefined; },
  stats: () => ({ starts, submissions }),
  lifecycle: () => ({ running: states.get("A")!.running, cancellable: states.get("A")!.cancellable, echoes: states.get("A")!.localSubmissionOrder.length }),
  lastInput: () => lastInput,
  complete: () => {
    let state = reducer(states.get("A")!, { type: "event", e: { kind: "session_operation", sessionOperation: terminal } });
    state = reducer(state, { type: "runtime_snapshot", snapshot: runtime(3) });
    states.set("A", state); paint();
  },
  coldRestore: () => {
    const history = sessionOperationHistoryMessage(`maintenance:${operation.operationId}`, terminal);
    states.set("A", reducer(reducer(initialState, { type: "history", messages: [history] }), { type: "runtime_snapshot", snapshot: runtime(3) }));
    paint();
  },
} });
paint();
