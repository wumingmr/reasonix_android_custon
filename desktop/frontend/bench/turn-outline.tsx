import { useSyncExternalStore } from "react";
import { createRoot } from "react-dom/client";
import { Transcript } from "../src/components/Transcript";
import { LocaleProvider } from "../src/lib/i18n";
import { ToastProvider } from "../src/lib/toast";
import { initialState, reducer, type Action, type State } from "../src/lib/useController";
import { getTranscriptStore } from "../src/lib/transcriptStore";
import { TranscriptSessionFollower } from "../src/lib/transcriptSessionFollower";
import { navigateHistoryTurn } from "../src/lib/historyTurnNavigation";
import { getTranscriptOutlineStore } from "../src/lib/transcriptOutlineStore";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import type { FollowRequest, HistoryOutlineRequest, HistoryWindowRequest } from "../src/generated/desktopContract.generated";
import "../src/styles.css";

const total = 25000, store = getTranscriptStore();
let tab = "outline-a", state = { ...initialState, meta: { sessionPath: tab } } as State;
let delayed = false, resume: (() => void) | undefined;
let completedReads = 0;
const reads: HistoryWindowRequest[] = [], outlineReads: HistoryOutlineRequest[] = [];
function page(turn: number, count = 16) {
  const end = Math.min(total, turn + count - 1);
  return { status: "ready", generation: "g", snapshotSequence: total, coverageSequence: total, totalTurns: total,
    hasOlder: turn > 1, hasNewer: end < total, olderCursor: turn > 1 ? String(turn) : "", newerCursor: end < total ? String(end + 1) : "",
    messages: Array.from({ length: end - turn + 1 }, (_, offset) => {
      const number = turn + offset;
      return ["user", "assistant"].map((role, side) => ({ messageId: `${role === "user" ? "u" : "a"}${number}`, role,
        position: number * 2 + side, version: 1, eventSequence: number, visibleTurn: number,
        inline: { id: `${role === "user" ? "u" : "a"}${number}`, role,
          content: role === "user" ? `Question ${number}` : `Answer ${number}\n\n${"A stable paragraph for reading and scrolling.\n\n".repeat(12)}` } }));
    }).flat() };
}
installDesktopHostStub({
  TranscriptFollowForTab: (_tab: string, req: FollowRequest) => req.close ? { protocolVersion: 2, subscription: _tab, changes: [], resetRequired: false }
    : req.subscription ? new Promise(() => {}) : { protocolVersion: 2, subscription: _tab, changes: [], resetRequired: false,
      snapshot: { protocolVersion: 1, snapshotId: "tail", identity: { sessionId: _tab, runtimeEpoch: "epoch", rewriteEpoch: 0, headId: "" },
        projectionRevision: 1, coveredThroughSeq: total, durableSeq: total, records: [], activeRecords: [], activeAttempts: [], before: 0,
        runtime: { status: "completed", pendingEvents: [], samplingCount: 0, toolCount: 0 }, hasOlder: true, totalRecords: 2, totalTurns: total, stale: false }, history: page(total, 1) },
  SessionHistoryOutlineForTab: (_tab: string, req: HistoryOutlineRequest) => {
    outlineReads.push(req); const start = req.startTurn ?? 1; const end = Math.min(total, start + (req.limit ?? 128) - 1);
    return { status: "ready", generation: "g", snapshotSequence: total, coverageSequence: total, totalTurns: total, done: end === total, nextTurn: end + 1,
      entries: Array.from({ length: end - start + 1 }, (_, index) => ({ messageId: `u${start + index}`, turn: start + index, position: (start + index) * 2, prompt: `Question ${start + index}`, answer: `Answer ${start + index}` })) };
  },
  SessionHistoryWindowForTab: async (_tab: string, req: HistoryWindowRequest) => {
    reads.push(req); if (delayed) { delayed = false; await new Promise<void>(done => { resume = done; }); }
    completedReads++;
    return page(req.anchor === "newest" ? total : Number(req.messageId?.slice(1) ?? req.cursor ?? 1));
  },
});
const dispatch = (action: Action) => { state = reducer(state, action); store.setState(tab, state); };
let follower = new TranscriptSessionFollower(tab, tab, false, dispatch, () => state);
await follower.start();
function Fixture() {
  const view = useSyncExternalStore(fn => store.subscribeState(tab, fn), () => state);
  return <div style={{ height: "100vh", display: "flex", flexDirection: "column" }}><Transcript
    items={view.items} tabId={tab} geometrySessionKey={tab} totalTurns={view.historyTotalTurns}
    historyStartTurn={view.historyStartTurn} historyEndTurn={view.historyEndTurn}
    hasOlderHistory={view.historyHasOlder} hasNewerHistory={view.historyHasNewer} onPrompt={() => {}}
    onNavigateToTurn={(target, current) => navigateHistoryTurn(tab, state, () => state, dispatch, target, current)}
    onLoadNewerHistory={async (_latest, current) => { const projection = await store.loadLatest(tab, tab, { current }); if (projection) dispatch({ type: "transcript_records", projection: { ...projection, removeIds: [] }, confirmedUsers: [] }); return projection ? "loaded" : "empty"; }}
  /></div>;
}
declare global { interface Window { turnOutlineFixture: { inspect(): unknown; delay(): void; resume(): void; switchSession(): Promise<void> } } }
window.turnOutlineFixture = {
  inspect: () => ({ reads, completedReads, outlineReads, users: state.items.filter(item => item.kind === "user").map(item => item.messageId),
    cacheEntries: getTranscriptOutlineStore().getView(tab).entries.size, tab, stateItems: state.items.length }),
  delay: () => { delayed = true; }, resume: () => { resume?.(); resume = undefined; },
  switchSession: async () => {
    const previous = tab; follower.stop(); tab = tab === "outline-a" ? "outline-b" : "outline-a";
    state = { ...initialState, meta: { sessionPath: tab } } as State;
    follower = new TranscriptSessionFollower(tab, tab, false, dispatch, () => state);
    await follower.start(); store.setState(previous, state);
  },
};
createRoot(document.getElementById("root")!).render(<LocaleProvider><ToastProvider><Fixture /></ToastProvider></LocaleProvider>);
