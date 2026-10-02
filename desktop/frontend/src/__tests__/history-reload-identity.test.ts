import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { getTranscriptStore } from "../lib/transcriptStore";
import { loadHistoryWindow, type HistoryWindowAction } from "../lib/historyWindowController";
import { initialState, reducer, type State } from "../lib/useController";

const dom = new JSDOM("", { url: "http://localhost/" });
globalThis.window = dom.window as unknown as Window & typeof globalThis;

let revision = 1;
const stub = installDesktopHostStub({
  SessionHistoryWindowForTab: async () => ({
    status: "ready",
    snapshotSequence: revision,
    generation: `generation-${revision}`,
    totalTurns: 30,
    hasOlder: true,
    olderCursor: "older-cursor",
    messages: [{ messageId: `user-${revision}`, position: 30, version: 1, role: "user", eventSequence: 30,
      visibleTurn: 30, preview: `revision ${revision}`, inline: { id: `user-${revision}`, role: "user", content: `revision ${revision}` } }],
  }),
});

try {
  const store = getTranscriptStore();
  for (const protocol of [1, 2] as const) {
    revision = 1;
    const tabId = `history-reload-tab-${protocol}`;
    const sessionPath = `/fixture/history-reload-${protocol}.jsonl`;
    const initial = await store.loadLatest(tabId, sessionPath);
    assert.equal(initial?.revision, 1);
    store.evictTab(tabId);
    revision = 2;
    let state: State = {
      ...initialState,
      transcriptProtocol: protocol,
      historyStartTurn: initial!.startTurn,
      historyTotalTurns: initial!.totalTurns,
      historyHasOlder: true,
      historyHasNewer: false,
      historyOlderLoading: false,
      historyNewerLoading: false,
      historyRevision: 1,
      historyDigest: "generation-1",
      running: false,
      meta: { label: "fixture", ready: true, eventChannel: "fixture", cwd: "/fixture", sessionPath,
        sessionRevision: 1, sessionDigest: "generation-1" },
    };
    const actions: HistoryWindowAction[] = [];
    const outcome = await loadHistoryWindow({
      tabId, direction: "older", trigger: "retry", state, requestSeq: 1,
      isCurrent: () => true,
      currentState: () => state,
      dispatch: action => {
        actions.push(action);
        state = reducer(state, action);
      },
    });
    assert.equal(outcome, "loaded", `protocol ${protocol}: refreshed page should replace an evicted old window: ${JSON.stringify(actions)}`);
    assert.equal(actions[actions.length - 1]?.type, protocol === 2 ? "transcript_records" : "history_replace");
    assert.equal(actions.find(action => action.type === "history_older_error"), undefined);
    assert.equal(state.historyRevision, 2);
    assert.equal(state.historyDigest, "generation-2");
    assert.equal(state.historyOlderLoading, false);
    assert.ok(state.items.some(item => item.kind === "user" && item.text === "revision 2"));
  }
} finally {
  stub.uninstall();
  dom.window.close();
}
