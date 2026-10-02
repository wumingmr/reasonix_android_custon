import assert from "node:assert/strict";
import { act } from "react";
import { createTranscriptHarness } from "./transcript-dom-harness";
import { installDesktopHostStub } from "./desktopHostStub";
import type { FollowRequest, TranscriptFollowResponse, HistoryOutlineRequest, HistoryWindowRequest } from "../generated/desktopContract.generated";
import type * as Controller from "../lib/useController";
import type * as Follower from "../lib/transcriptSessionFollower";
import type * as Navigation from "../lib/historyTurnNavigation";

for (const remote of [false, true]) {
  const harness = await createTranscriptHarness({ deterministic: true, viewportHeight: 300, rowHeight: 80 });
  const tab = remote ? "remote-outline" : "local-outline";
  const prefix = remote ? "Remote" : "";
  const outlineRequests: HistoryOutlineRequest[] = [];
  const windows: HistoryWindowRequest[] = [];
  let block: ((request: HistoryWindowRequest) => Promise<void>) | undefined;
  const message = (turn: number) => ({ messageId: `u${turn}`, position: turn, version: 1, role: "user", eventSequence: turn,
    visibleTurn: turn, preview: `question ${turn}`, inline: { id: `u${turn}`, role: "user", content: `question ${turn}` } });
  const baseline: TranscriptFollowResponse = {
    protocolVersion: 2, subscription: tab, changes: [], resetRequired: false,
    snapshot: { protocolVersion: 1, snapshotId: "tail", identity: { sessionId: tab, runtimeEpoch: "epoch", rewriteEpoch: 0, headId: "" },
      projectionRevision: 10, coveredThroughSeq: 6, durableSeq: 6, records: [], activeRecords: [], activeAttempts: [],
      runtime: { status: "completed", pendingEvents: [], samplingCount: 0, toolCount: 0 }, before: 0, hasOlder: true, totalRecords: 1, totalTurns: 6, stale: false },
    history: { status: "ready", snapshotSequence: 6, coverageSequence: 6, generation: "durable", totalTurns: 6,
      hasOlder: true, hasNewer: false, olderCursor: "older", messages: [message(6)] },
  };
  const stub = installDesktopHostStub({
    [`${prefix}TranscriptFollowForTab`]: (_tab: string, request: FollowRequest) => request.close
      ? Promise.resolve({ protocolVersion: 2, changes: [], resetRequired: false, subscription: tab })
      : request.subscription ? new Promise(() => {}) : Promise.resolve(baseline),
    [`${prefix}SessionHistoryOutlineForTab`]: async (_tab: string, request: HistoryOutlineRequest) => {
      outlineRequests.push(request);
      return { status: "ready", generation: "durable", snapshotSequence: 6, coverageSequence: 6, totalTurns: 6, done: true, nextTurn: 7,
        entries: Array.from({ length: 6 }, (_, i) => ({ messageId: `u${i + 1}`, turn: i + 1, position: i + 1, prompt: `question ${i + 1}`, answer: `answer ${i + 1}` })) };
    },
    [`${prefix}SessionHistoryWindowForTab`]: async (_tab: string, request: HistoryWindowRequest) => {
      windows.push(request);
      await block?.(request);
      const turn = request.messageId ? Number(request.messageId.slice(1)) : 5;
      return { status: "ready", snapshotSequence: 6, coverageSequence: 6, generation: "durable", totalTurns: 6,
        hasOlder: turn > 1, hasNewer: turn < 6, olderCursor: turn > 1 ? "older" : "", newerCursor: turn < 6 ? "newer" : "", messages: [message(turn)] };
    },
  });
  const controller = await harness.loadModule<typeof Controller>("/src/lib/useController.ts");
  const { TranscriptSessionFollower } = await harness.loadModule<typeof Follower>("/src/lib/transcriptSessionFollower.ts");
  const { navigateHistoryTurn } = await harness.loadModule<typeof Navigation>("/src/lib/historyTurnNavigation.ts");
  const backend = await harness.loadModule<typeof import("../lib/canonicalTranscriptBackend")>("/src/lib/canonicalTranscriptBackend.ts");
  backend.setTranscriptBindingIdentity(() => remote ? "remote" : "local");
  let state = { ...controller.initialState, meta: { sessionPath: tab } } as Controller.State;
  const dispatch = (action: Controller.Action) => { state = controller.reducer(state, action); };
  const follower = new TranscriptSessionFollower(tab, tab, remote, dispatch, () => state);
  const render = () => harness.render(state.items, { tabId: tab, hostId: remote ? "host" : "local", geometrySessionKey: tab,
    totalTurns: state.historyTotalTurns, historyStartTurn: state.historyStartTurn, hasOlderHistory: state.historyHasOlder, hasNewerHistory: state.historyHasNewer,
    onNavigateToTurn: (target: Navigation.TurnNavigationTarget, current: () => boolean) => navigateHistoryTurn(tab, state, () => state, dispatch, target, current) });
  try {
    await follower.start(); await render(); await harness.settle();
    await harness.waitFor(() => harness.container.querySelectorAll('[data-nav-turn="m:u1"]').length === 1, "production directory initialization");
    assert.equal(harness.container.querySelectorAll("[data-nav-turn]").length, 6, "one body turn still exposes the full rail");
    assert.equal(state.items.filter(item => item.kind === "user").length, 1);
    for (const turn of [1, 3, 6]) {
      await act(async () => { harness.container.querySelector<HTMLElement>(`[data-nav-turn="m:u${turn}"]`)!.click(); });
      await harness.waitFor(() => state.items.some(item => item.kind === "user" && item.messageId === `u${turn}`), "target window publication");
      await render(); await harness.settle();
      assert.ok(harness.container.querySelector(`[data-chat-anchor-key="m:u${turn}"]`), `turn ${turn} mounted`);
    }
    assert.equal(windows.length, 3, "each far jump requests one window, never intervening pages");
    assert.ok(windows.every(request => request.generation === "durable" && request.snapshotSequence === 6));
    assert.ok(outlineRequests.length <= 2, "body navigation does not refetch the whole directory");
    const { getTranscriptStore } = await harness.loadModule<typeof import("../lib/transcriptStore")>("/src/lib/transcriptStore.ts");
    const store = getTranscriptStore();
    let resume!: () => void;
    const gate = new Promise<void>(resolve => { resume = resolve; });
    block = request => request.cursor ? gate : Promise.resolve();
    const oldPage = store.loadOlder(tab, tab);
    await harness.waitFor(() => windows.length === 4, "older read started");
    const replacement = await store.prepareTargetWindow(tab, tab, { anchor: "message", messageId: "u3", direction: "newer", limit: 32 }, () => true);
    assert.equal(replacement.status, "loaded");
    if (replacement.status === "loaded") {
      assert.equal(replacement.current(), true);
      replacement.prepared.commit();
    }
    resume();
    assert.equal(await oldPage, undefined, "late ordinary paging cannot overwrite a target window");
    const targetGate = new Promise<void>(resolve => { resume = resolve; });
    block = request => request.messageId ? targetGate : Promise.resolve();
    const superseded = store.prepareTargetWindow(tab, tab, { anchor: "message", messageId: "u1", direction: "newer", limit: 32 }, () => true);
    await harness.waitFor(() => windows.length === 6, "target read started");
    await store.loadNewer(tab, tab);
    resume();
    assert.equal((await superseded).status, "cancelled", "newer paging supersedes a pending target");
    const latestGate = new Promise<void>(resolve => { resume = resolve; });
    block = request => request.anchor === "newest" ? latestGate : Promise.resolve();
    const pendingLatest = store.loadLatest(tab, tab);
    await harness.waitFor(() => windows.length === 8, "latest read started");
    const preparedTarget = await store.prepareTargetWindow(tab, tab, { anchor: "message", messageId: "u1", direction: "newer", limit: 32 }, () => true);
    assert.equal(preparedTarget.status, "loaded");
    resume();
    assert.equal(await pendingLatest, undefined, "older latest read is obsolete before target publication");
    const { loadHistoryWindow } = await harness.loadModule<typeof import("../lib/historyWindowController")>("/src/lib/historyWindowController.ts");
    let readerCurrent = true;
    const cancelGate = new Promise<void>(resolve => { resume = resolve; });
    block = () => cancelGate;
    const cancelledLatest = loadHistoryWindow({ tabId: tab, direction: "latest", trigger: "return-latest", state,
      requestSeq: 1, isCurrent: () => true, readerCurrent: () => readerCurrent, currentState: () => state, dispatch });
    await harness.waitFor(() => windows.length === 10, "reader-cancellable latest read started");
    const beforeCancellation = state.items;
    readerCurrent = false; resume();
    assert.equal(await cancelledLatest, "empty");
    assert.equal(state.items, beforeCancellation, "reader cancellation preserves the visible body");
    assert.equal(state.historyNewerLoading, false);
    assert.equal(state.historyNewerError, "", "intent cancellation is not a network failure");
  } finally { follower.stop(); await harness.close(); stub.uninstall(); }
}
console.log("production Follow v2: local and remote full directory, one-turn tail and direct jumps passed");
