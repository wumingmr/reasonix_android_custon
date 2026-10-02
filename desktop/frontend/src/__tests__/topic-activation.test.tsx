// Run: tsx src/__tests__/topic-activation.test.tsx
//
// Ticketed topic activation (StartTopicActivation + "topic:activation"
// events): rapid A→B→C navigation with out-of-order lifecycle events hydrates
// only the last click; runtime failure leaves independent history readable; a
// terminal event that beats the ticket resolution is stashed and replayed;
// the legacy agent:ready flow and the tab:meta refresh push still work.

import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import type { AppBindings } from "../lib/bridge";
import { useController } from "../lib/useController";
import { getTranscriptStore } from "../lib/transcriptStore";
import { historySliceFromMessages } from "./mockHistorySlice";
import type {
  BalanceInfo,
  CheckpointMeta,
  ContextInfo,
  EffortInfo,
  HistoryMessage,
  HistorySliceRequest,
  JobView,
  Meta,
  TabMeta,
  TabMetaRefreshEvent,
  TopicActivationEvent,
  TopicActivationRequest,
  WireEvent,
} from "../lib/types";
import { installDesktopHostStub } from "./desktopHostStub";
import { makeMockSessionReaderBindings } from "../lib/sessionReaderBridge";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function eq(actual: unknown, expected: unknown, label: string) {
  ok(actual === expected, `${label}${actual === expected ? "" : `: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`}`);
}

function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    await act(async () => {
      await flushPromises();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function tabMeta(id: string, overrides: Partial<TabMeta> = {}): TabMeta {
  const workspaceRoot = `/repo/${id}`;
  return {
    id,
    scope: "project",
    workspaceRoot,
    workspaceName: id,
    workspacePath: workspaceRoot,
    gitBranch: "main",
    topicId: `topic-${id}`,
    topicTitle: id,
    sessionPath: `${workspaceRoot}/sessions/${id}.jsonl`,
    label: `model-${id}`,
    ready: true,
    running: false,
    mode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    active: false,
    cwd: workspaceRoot,
    ...overrides,
  };
}

function metaFor(tab: TabMeta, overrides: Partial<Meta> = {}): Meta {
  return {
    label: tab.label,
    ready: tab.ready,
    startupErr: tab.startupErr,
    eventChannel: "agent:event",
    cwd: tab.cwd || tab.workspaceRoot,
    workspaceRoot: tab.workspaceRoot,
    workspaceName: tab.workspaceName,
    workspacePath: tab.workspacePath,
    sessionPath: tab.sessionPath,
    session: tab.session,
    sessionGeneration: tab.sessionGeneration,
    gitBranch: tab.gitBranch,
    autoApproveTools: false,
    bypass: false,
    collaborationMode: tab.collaborationMode ?? "normal",
    toolApprovalMode: tab.toolApprovalMode ?? "ask",
    tokenMode: tab.tokenMode ?? "full",
    goal: "",
    goalStatus: "stopped",
    ...overrides,
  };
}

console.log("\ntopic activation (ticketed)");

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
  pretendToBeVisual: true,
  url: "http://localhost/",
});
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.CustomEvent = dom.window.CustomEvent;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);

const context: ContextInfo = { used: 12, window: 100, sessionTokens: 12 };
const effort: EffortInfo = { supported: true, current: "auto", default: "auto", levels: ["auto"] };
const balance: BalanceInfo = { available: false, display: "" };
const jobs: JobView[] = [];
const checkpoints: CheckpointMeta[] = [];
const tabA = tabMeta("tab-a", { active: true, ready: false });
const tabB = tabMeta("tab-b");
const tabC = tabMeta("tab-c");
const tabR = tabMeta("tab-r", { running: true, cancellable: true });
const tabAsk = tabMeta("tab-ask", { workspaceRoot: "/work/ask", topicId: "topic-ask", sessionPath: "/sessions/ask.jsonl" });
let backendActiveId = "tab-a";
const tabsById = new Map([tabA, tabB, tabC, tabR, tabAsk].map((tab) => [tab.id, tab]));
// requestId the controller issued per tab, recorded by the mock.
const requestIdByTab = new Map<string, string>();
// When true, the mock emits starting+ready synchronously BEFORE returning the
// ticket (exercises the terminal-event stash path).
let eagerActivationEvents = false;
let beforeActivationReturn: (() => Promise<void>) | undefined;
let failedHistoryTabId = "";
let historyRequests = 0;
let historyGate: { tabId: string; promise: Promise<void> } | undefined;
const transientHistoryFailures = new Map<string, { remaining: number; beforeThrow?: () => Promise<void> }>();
let failSetActiveTabId = "";
let restoredTabSeq = 0;
let followStarts = 0;
const followTabs: string[] = [];
const readerBindings = makeMockSessionReaderBindings();

function emitActivation(event: TopicActivationEvent): void {
  desktopStub.emit("topic:activation", event);
}

function historyFor(tabID: string): HistoryMessage[] {
  if (tabID === "tab-r") return [{ role: "user", content: "帮我查询，现在我链接的 deep" }];
  if (tabID.startsWith("tab-c-restored-")) return [{ role: "user", content: "history tab-c" }];
  return [{ role: "user", content: `history ${tabID}` }];
}

function hasHistory(tabID: string): boolean {
  return controller?.state.items.some((item) => item.kind === "user" && item.text === `history ${tabID}`) ?? false;
}

const desktopStub = installDesktopHostStub(({
  main: {
    App: {
      async TranscriptFollowForTab(tabID, request) {
        if (!request.subscription && !request.close) {
          followStarts++;
          followTabs.push(tabID);
          if (tabsById.get(tabID)?.startupErr) throw new Error("runtime lease blocked");
        }
        return readerBindings.TranscriptFollowForTab.call(this, tabID, request);
      },
      RegisterNavigationIntent: async () => {},
      ListTabs: async () => Array.from(tabsById.values()).map((tab) => ({ ...tab, active: tab.id === backendActiveId })),
      MetaForTab: async (tabID: string) => metaFor(tabsById.get(tabID) ?? tabA),
      ContextUsageForTab: async () => context,
      EffortForTab: async () => effort,
      BalanceForTab: async () => balance,
      JobsForTab: async () => jobs,
      CheckpointsForTab: async () => checkpoints,
      ForkTargetsForTab: async () => ({ targets: [], verifiable: false }),
      HistoryForTab: async (tabID: string) => historyFor(tabID),
      HistorySliceForTab: async (tabID: string, req: HistorySliceRequest) => {
        historyRequests++;
        if (historyGate?.tabId === tabID) await historyGate.promise;
        if (tabID === failedHistoryTabId) throw new Error(`/private/${tabID}/history.jsonl could not be read`);
        const transientFailure = transientHistoryFailures.get(tabID);
        if (transientFailure && transientFailure.remaining > 0) {
          transientFailure.remaining -= 1;
          await transientFailure.beforeThrow?.();
          throw new Error("session runtime is still publishing its history");
        }
        return historySliceFromMessages(tabID, historyFor(tabID), req,
          tabsById.get(tabID)?.startupErr ? { digest: `native-digest-${tabID}` } : {});
      },
      HistoryCheckpointTurnsForTab: async () => [],
      StartTopicActivation: async (req: TopicActivationRequest) => {
        const target = Array.from(tabsById.values()).find((tab) => tab.workspaceRoot === req.workspaceRoot && tab.topicId === req.topicId) ?? tabA;
        backendActiveId = target.id;
        const requestId = req.requestId || `mock-activation-${target.id}`;
        requestIdByTab.set(target.id, requestId);
        await beforeActivationReturn?.();
        if (eagerActivationEvents) {
          emitActivation({ requestId, tabId: target.id, phase: "starting" });
          emitActivation({ requestId, tabId: target.id, phase: "ready" });
        }
        return { requestId, tabId: target.id, meta: { ...target, active: true } };
      },
      SetActiveTab: async (tabID: string) => {
        if (tabID === failSetActiveTabId) throw new Error("source tab was already pruned");
        backendActiveId = tabID;
      },
      OpenTopicSession: async (_scope: string, _workspaceRoot: string, _topicID: string, sessionPath: string) => {
        const source = Array.from(tabsById.values()).find((tab) => tab.sessionPath === sessionPath) ?? tabA;
        const restored = tabMeta(`${source.id}-restored-${++restoredTabSeq}`, {
          ...source,
          id: `${source.id}-restored-${restoredTabSeq}`,
          active: true,
        });
        tabsById.set(restored.id, restored);
        backendActiveId = restored.id;
        return restored;
      },
      ReplayPendingPrompts: async () => {},
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);

type Controller = ReturnType<typeof useController>;
let controller: Controller | undefined;

function Probe() {
  controller = useController();
  return null;
}

const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");
const root = createRoot(rootEl);

await act(async () => {
  root.render(<Probe />);
  await flushPromises();
});
await waitFor("initial tab", () => controller?.activeTabId === "tab-a" && hasHistory("tab-a"));
eq(controller?.state.meta?.ready, false, "startup history is readable while execution is still recovering");
eq(controller?.state.hydrating, false, "cold startup settles the history window independently");
ok(controller?.state.transcriptProtocol !== 2, "cold startup does not invent a live Follow subscription");
tabsById.set(tabA.id, { ...tabA, ready: true });
await act(async () => { desktopStub.emit("agent:ready", tabA.id); await flushPromises(); });
await waitFor("startup live handoff", () => controller?.state.transcriptProtocol === 2 && controller.state.meta?.ready === true);
eq(controller?.state.items.filter(item => item.kind === "user" && item.text === "history tab-a").length, 1,
  "runtime handoff replaces the cold cut without duplicating history");

// ── rapid A→B→C with out-of-order events: only C hydrates ──────────────────
await act(async () => {
  await controller?.activateTopic("project", tabB.workspaceRoot, tabB.topicId ?? "");
  await flushPromises();
});
eq(controller?.activeTabId, "tab-b", "B applies optimistically on its ticket");
eq(controller?.state.hydrating, false, "B becomes readable before its runtime terminal event");
ok(hasHistory("tab-b"), "B publishes canonical history while runtime activation is still pending");

await act(async () => {
  await controller?.activateTopic("project", tabC.workspaceRoot, tabC.topicId ?? "");
  await flushPromises();
});
eq(controller?.activeTabId, "tab-c", "C applies optimistically over B");
eq(controller?.state.hydrating, false, "C history also settles independently of runtime activation");
ok(hasHistory("tab-c"), "C is readable before its runtime terminal event");

// Out of order: B's terminal events arrive (superseded) before C's ready.
await act(async () => {
  emitActivation({ requestId: requestIdByTab.get("tab-b") ?? "", tabId: "tab-b", phase: "cancelled" });
  emitActivation({ requestId: requestIdByTab.get("tab-b") ?? "", tabId: "tab-b", phase: "ready" });
  emitActivation({ requestId: requestIdByTab.get("tab-a") ?? "", tabId: "tab-a", phase: "ready" });
  await flushPromises();
});
eq(controller?.activeTabId, "tab-c", "superseded terminal events do not flip the visible tab");
ok(!hasHistory("tab-b") && !hasHistory("tab-a"), "superseded terminal events never hydrate");
eq(controller?.state.hydrating, false, "C remains readable while it waits for its own runtime terminal event");

await act(async () => {
  emitActivation({ requestId: requestIdByTab.get("tab-c") ?? "", tabId: "tab-c", phase: "starting" });
  await flushPromises();
});
eq(controller?.state.hydrating, false, "runtime starting does not hide already-readable history");
ok(hasHistory("tab-c"), "runtime starting preserves the readable C transcript");
await act(async () => {
  emitActivation({ requestId: requestIdByTab.get("tab-c") ?? "", tabId: "tab-c", phase: "ready" });
  await flushPromises();
});
await waitFor("C hydrates on its ready", () => hasHistory("tab-c") && controller?.state.hydrating === false);
ok(!hasHistory("tab-b"), "only the last click's history is visible");

// ── runtime failure preserves independently readable history ───────────────
await act(async () => {
  await controller?.activateTopic("project", tabA.workspaceRoot, tabA.topicId ?? "");
  await flushPromises();
});
eq(controller?.activeTabId, "tab-a", "A applies optimistically");
await act(async () => {
  emitActivation({ requestId: requestIdByTab.get("tab-a") ?? "", tabId: "tab-a", phase: "failed", error: "session failed to start" });
  await flushPromises();
});
eq(controller?.activeTabId, "tab-a", "failed runtime activation keeps the selected readable target");
eq(controller?.state.hydrating, false, "failed runtime activation does not re-enter history hydration");
ok(hasHistory("tab-a"), "failed runtime activation retains the target transcript");
eq(controller?.state.meta?.ready, false, "failed runtime activation keeps write actions fenced");
ok(Boolean(controller?.state.meta?.startupErr), "failed runtime activation exposes a safe retry state");
const failureNotice = controller?.state.items.findLast((item) => item.kind === "notice");
ok(Boolean(failureNotice && failureNotice.kind === "notice" && !failureNotice.text.includes("session failed to start")), "failure notice is sanitized before it reaches the readable target");

let releaseHistory!: () => void;
getTranscriptStore().evictTab(tabB.id);
historyGate = { tabId: tabB.id, promise: new Promise<void>(resolve => { releaseHistory = resolve; }) };
await act(async () => {
  await controller?.activateTopic("project", tabB.workspaceRoot, tabB.topicId ?? "");
  emitActivation({ requestId: requestIdByTab.get(tabB.id) ?? "", tabId: tabB.id, phase: "failed" });
  await flushPromises();
});
eq(controller?.state.hydrateError, undefined, "runtime failure is not a history read failure");
eq(controller?.state.hydrating, true, "history preparation continues after runtime failure");
tabsById.set(tabB.id, { ...tabB, ready: false, startupErr: "lease blocked", runtime: { phase: "lease_blocked", epoch: "blocked" } });
const followsBeforeBlockedRefresh = followStarts;
const readsBeforeBlockedRefresh = historyRequests;
await act(async () => { desktopStub.emit("agent:ready", tabB.id); await flushPromises(); });
eq(followStarts, followsBeforeBlockedRefresh, "blocked metadata refresh never starts live Follow");
eq(historyRequests, readsBeforeBlockedRefresh, "blocked metadata refresh joins the pending cold read");
await act(async () => { releaseHistory(); await flushPromises(); });
await waitFor("history succeeds after runtime failure", () => hasHistory(tabB.id));
eq(controller?.state.hydrateError, undefined, "a late valid baseline clears the history error");
eq(controller?.state.meta?.ready, false, "late history cannot clear the failed runtime write fence");
historyGate = undefined;
const readsAfterBlockedHistory = historyRequests;
const itemsAfterBlockedHistory = JSON.stringify(controller?.state.items);
const mutationAfterBlockedHistory = controller?.state.historyMutation.seq;
await act(async () => { await controller?.syncActiveTab(false, true, { preserveCachedHistory: true }); });
eq(historyRequests, readsAfterBlockedHistory, "settled blocked metadata refresh keeps the certified window without a latest-page read");
eq(JSON.stringify(controller?.state.items), itemsAfterBlockedHistory, "passive blocked refresh preserves the reader's current items");
eq(controller?.state.historyMutation.seq, mutationAfterBlockedHistory, "passive blocked refresh does not publish another history replacement");
const blockedFollowsBeforeRebuild = followTabs.filter(id => id === tabB.id).length;
await act(async () => {
  desktopStub.emit("runtime:rebuilt", tabB.id, "blocked-rebuilt");
  desktopStub.emit("runtime:rebuilt");
  await flushPromises();
});
eq(followTabs.filter(id => id === tabB.id).length, blockedFollowsBeforeRebuild, "rebuild notifications do not follow a blocked runtime");
ok(hasHistory(tabB.id), "rebuild notifications retain independent readable history");
// An actual read error, independently of execution recovery, settles locally.
getTranscriptStore().evictTab(tabB.id);
failedHistoryTabId = tabB.id;
await act(async () => { await controller?.activateTopic("project", tabB.workspaceRoot, tabB.topicId ?? ""); await flushPromises(); });
await waitFor("cold read error", () => Boolean(controller?.state.hydrateError));
eq(controller?.activeTabId, tabB.id, "cold read error keeps the selected blocked session");
eq(controller?.state.hydrating, false, "cold read failure does not leave a permanent skeleton");
failedHistoryTabId = "";
tabsById.set(tabB.id, tabB);

await act(async () => {
  await controller?.activateTopic("project", tabC.workspaceRoot, tabC.topicId ?? "");
  emitActivation({ requestId: requestIdByTab.get("tab-c") ?? "", tabId: "tab-c", phase: "ready" });
  await flushPromises();
});
await waitFor("C is restored as the committed source", () => controller?.activeTabId === "tab-c" && hasHistory("tab-c"));

// ── activation succeeds but target history fails: source still wins ─────────
failedHistoryTabId = "tab-a";
failSetActiveTabId = "tab-c";
await act(async () => {
  await controller?.activateTopic("project", tabA.workspaceRoot, tabA.topicId ?? "");
  await flushPromises();
  emitActivation({ requestId: requestIdByTab.get("tab-a") ?? "", tabId: "tab-a", phase: "ready" });
  await flushPromises();
});
await waitFor("history failure rebinds the pruned committed source", () => controller?.activeTabId?.startsWith("tab-c-restored-") ?? false);
ok(hasHistory("tab-c"), "target history failure retains the source controller transcript");
const historyFailureNotice = controller?.state.items.findLast((item) => item.kind === "notice");
ok(!(historyFailureNotice?.kind === "notice" && historyFailureNotice.text.includes("/private/")), "target history failure does not expose a local path");
failedHistoryTabId = "";
failSetActiveTabId = "";

// ── a terminal event that beats the ticket is stashed and replayed ──────────
eagerActivationEvents = true;
await act(async () => {
  await controller?.activateTopic("project", tabB.workspaceRoot, tabB.topicId ?? "");
  await flushPromises();
});
await waitFor("stashed ready hydrates after the ticket lands", () => controller?.activeTabId === "tab-b" && hasHistory("tab-b"));
eagerActivationEvents = false;

// ── legacy agent:ready flow still works for non-ticketed tabs ───────────────
await act(async () => {
  desktopStub.emit("agent:ready", "tab-b");
  await flushPromises();
});
await waitFor("legacy ready keeps the session", () => controller?.activeTabId === "tab-b" && hasHistory("tab-b"));

// ── tab:meta refresh push merges; wrong session is fenced out ───────────────
await act(async () => {
  desktopStub.emit("tab:meta", { tabId: "tab-b", meta: metaFor(tabB, { gitBranch: "feature/x", imageInputEnabled: true }) });
  await flushPromises();
});
eq(controller?.state.meta?.gitBranch, "feature/x", "tab:meta merges the refreshed git branch");
eq(controller?.state.meta?.imageInputEnabled, true, "tab:meta merges the refreshed image-input capability");
await act(async () => {
  desktopStub.emit("tab:meta", { tabId: "tab-b", meta: metaFor(tabB, { gitBranch: "stale", sessionPath: "/elsewhere/other.jsonl" }) });
  await flushPromises();
});
eq(controller?.state.meta?.gitBranch, "feature/x", "tab:meta for a different session binding is discarded");

// ── switch away from a thinking session and back: keep its transcript ──────
await act(async () => {
  await controller?.activateTopic("project", tabR.workspaceRoot, tabR.topicId ?? "");
  await flushPromises();
});
await act(async () => {
  emitActivation({ requestId: requestIdByTab.get("tab-r") ?? "", tabId: "tab-r", phase: "ready" });
  await flushPromises();
});
await waitFor("running session hydrates on first open", () =>
  controller?.activeTabId === "tab-r" &&
  controller.state.running === true &&
  controller.state.hydrating === false &&
  (controller.state.items.some((item) => item.kind === "user" && item.text === "帮我查询，现在我链接的 deep") ?? false),
);
eq(controller?.state.running, true, "reattached thinking session stays marked running");
eq(controller?.state.hydrating, false, "running-session hydrate settles instead of leaving Welcome");

const liveState = getTranscriptStore().states.get(tabR.id)!;
await act(async () => {
  getTranscriptStore().setState(tabR.id, { ...liveState, running: true, pendingUser: "unsaved live prompt" });
  await flushPromises();
});
const beforeReselect = historyRequests;
await act(async () => {
  await controller?.activateTopic("project", tabR.workspaceRoot, tabR.topicId ?? "");
  await flushPromises();
});
eq(historyRequests, beforeReselect, "reselecting a live surface never overlays it with an older durable baseline");
eq(controller?.state.pendingUser, "unsaved live prompt", "reselecting preserves the optimistic prompt");
await act(async () => { getTranscriptStore().setState(tabR.id, liveState); await flushPromises(); });

await act(async () => {
  await controller?.activateTopic("project", tabB.workspaceRoot, tabB.topicId ?? "");
  await flushPromises();
});
await act(async () => {
  emitActivation({ requestId: requestIdByTab.get("tab-b") ?? "", tabId: "tab-b", phase: "ready" });
  await flushPromises();
});
await waitFor("idle session replaces the thinking surface", () => hasHistory("tab-b") && controller?.state.hydrating === false);

await act(async () => {
  await controller?.activateTopic("project", tabR.workspaceRoot, tabR.topicId ?? "");
  await flushPromises();
});
eq(controller?.activeTabId, "tab-r", "switch-back selects the thinking session");
eq(
  controller?.state.hydratePlaceholderItems?.some((item) => item.kind === "user" && item.text === "history tab-b") ?? false,
  false,
  "switch-back does not present the other session as a hydration placeholder",
);
await act(async () => {
  emitActivation({ requestId: requestIdByTab.get("tab-r") ?? "", tabId: "tab-r", phase: "ready" });
  await flushPromises();
});
await waitFor("switch-back restores the thinking transcript", () =>
  controller?.activeTabId === "tab-r" &&
  controller.state.running === true &&
  controller.state.hydrating === false &&
  (controller.state.items.some((item) => item.kind === "user" && item.text === "帮我查询，现在我链接的 deep") ?? false),
);
eq(controller?.state.running, true, "switch-back keeps the composer in the live turn");
eq(controller?.state.items.some((item) => item.kind === "user" && item.text === "history tab-b") ?? false, false, "switch-back does not keep the other session as the visible transcript");
eq(controller?.state.hydratePlaceholderItems?.length ?? 0, 0, "switch-back clears the foreign placeholder after live history lands");

// A ready Ask runtime can race the first history read while its session is
// being published. One transient read failure must recover in the same click
// instead of restoring the source and making the user click B again.
tabsById.set(tabAsk.id, { ...tabAsk, running: true, pendingPrompt: true, cancellable: true });
let markHistoryStarted: (() => void) | undefined;
const historyStarted = new Promise<void>((resolve) => { markHistoryStarted = resolve; });
let releaseHistoryFailure: (() => void) | undefined;
const historyFailureGate = new Promise<void>((resolve) => { releaseHistoryFailure = resolve; });
transientHistoryFailures.set(tabAsk.id, {
  remaining: 1,
  beforeThrow: async () => { markHistoryStarted?.(); await historyFailureGate; },
});
await act(async () => {
  await controller?.activateTopic("project", tabAsk.workspaceRoot, tabAsk.topicId ?? "");
  desktopStub.emit("agent:event", {
    kind: "ask_request",
    tabId: tabAsk.id,
    ask: { id: "ask-tab-ask", questions: [{ id: "choice", prompt: "Choose a repair", options: [] }] },
  });
  await flushPromises();
});
eq(controller?.state.ask?.id, undefined, "Ask waits for the consistent activation snapshot");
await act(async () => {
  // Production emits agent:ready before topic:activation ready. Hold the
  // startup hydration open so activation-ready must supersede it without
  // resetting the live Ask.
  desktopStub.emit("agent:ready", tabAsk.id);
  await historyStarted;
  emitActivation({ requestId: requestIdByTab.get(tabAsk.id) ?? "", tabId: tabAsk.id, phase: "ready" });
  releaseHistoryFailure?.();
  await flushPromises();
});
for (let attempt = 0; attempt < 10; attempt += 1) {
  await act(async () => { await flushPromises(); });
}
eq(controller?.activeTabId, tabAsk.id, "transient Ask history failure stays on the selected session");
ok(hasHistory(tabAsk.id), "transient Ask history failure retries without a second click");
eq(controller?.state.pendingPrompt, true, "transient Ask history retry remains blocked on user input");
eq(controller?.state.running, true, "transient Ask history retry remains running");
eq(controller?.state.ask?.id, "ask-tab-ask", "transient Ask history retry preserves the decision card");

await act(async () => {
  root.unmount();
});

// A restored legacy surface reuses its tab ID when preparation publishes a
// canonical identity. Exercise both terminal/ticket orders after a failed
// startup baseline, including passive agent-ready arriving before the ticket.
for (const eager of [false, true]) {
  const restored = tabMeta(`restored-${eager}`, { ready: false, sessionPath: "C:\\fixture\\legacy.jsonl" });
  tabsById.clear();
  tabsById.set(restored.id, restored);
  backendActiveId = restored.id;
  failedHistoryTabId = restored.id;
  const restoredRoot = createRoot(rootEl);
  await act(async () => { restoredRoot.render(<Probe />); await flushPromises(); });
  await waitFor("restored startup baseline fails", () => controller?.activeTabId === restored.id && !!controller.state.hydrateError);
  tabsById.set(restored.id, { ...restored, ready: true, sessionPath: "", session: { hostId: "local", sessionId: `canonical-${eager}` }, sessionGeneration: 1 });
  failedHistoryTabId = "";
  eagerActivationEvents = eager;
  beforeActivationReturn = async () => { desktopStub.emit("agent:ready", restored.id); await flushPromises(); };
  await act(async () => {
    await controller?.activateTopic(restored.scope, restored.workspaceRoot, restored.topicId ?? "", `session-id:canonical-${eager}`);
    if (!eager) emitActivation({ requestId: requestIdByTab.get(restored.id) ?? "", tabId: restored.id, phase: "ready" });
    await flushPromises();
  });
  await waitFor("restored canonical history is visible", () => hasHistory(restored.id) && !controller?.state.hydrating);
  eq(controller?.state.hydrateError, undefined, `same-tab canonical adoption clears the failed startup baseline (eager=${eager})`);
  eq(controller?.state.meta?.session?.sessionId, `canonical-${eager}`, "the restored surface retains its formal session identity");
  eq(controller?.state.transcriptProtocol, 2, "canonical adoption installs the live Follow projection");
  await act(async () => restoredRoot.unmount());
}
dom.window.close();

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
