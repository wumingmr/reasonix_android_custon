// Run: tsx src/__tests__/fork-child-memory.test.tsx
// A turn that already produced a child session is never forked twice: the child
// is remembered per turn, so a repeat fork re-reports it instead of creating a
// second one, while a fork of another turn still creates its own.
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import type { AppBindings } from "../lib/bridge";
import { createForkTargetsRefresh } from "../lib/forkTurn";
import { useController } from "../lib/useController";
import { historySliceFromMessages } from "./mockHistorySlice";
import type { BalanceInfo, CheckpointMeta, ContextInfo, EffortInfo, HistoryMessage, HistorySliceRequest, JobView, Meta, TabMeta } from "../lib/types";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0;
let failed = 0;
function ok(value: unknown, label: string) {
  if (value) { passed += 1; process.stdout.write(`  PASS  ${label}\n`); }
  else { failed += 1; process.stdout.write(`  FAIL  ${label}\n`); }
}

function flushPromises(ms = 0): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    await act(async () => { await flushPromises(10); });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function tabMeta(id: string): TabMeta {
  return {
    id,
    scope: "project",
    workspaceRoot: "/repo",
    workspaceName: "repo",
    workspacePath: "/repo",
    gitBranch: "main",
    topicId: `topic-${id}`,
    topicTitle: id,
    sessionPath: `/repo/sessions/${id}.jsonl`,
    label: "model",
    ready: true,
    running: false,
    mode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    active: id === "tab-a",
    cwd: "/repo",
  };
}

function meta(tabId: string): Meta {
  return {
    label: "model",
    ready: true,
    eventChannel: "agent:event",
    cwd: "/repo",
    workspaceRoot: "/repo",
    workspaceName: "repo",
    workspacePath: "/repo",
    sessionPath: `/repo/sessions/${tabId}.jsonl`,
    gitBranch: "main",
    autoApproveTools: false,
    bypass: false,
    collaborationMode: "normal",
    toolApprovalMode: "ask",
    tokenMode: "full",
    goal: "",
    goalStatus: "stopped",
  };
}

console.log("\nfork child memory");

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.dom ?? dom.window.navigator });
globalThis.Node = dom.window.Node;
globalThis.HTMLElement = dom.window.HTMLElement;
globalThis.Event = dom.window.Event;
globalThis.CustomEvent = dom.window.CustomEvent;
globalThis.KeyboardEvent = dom.window.KeyboardEvent;
globalThis.MouseEvent = dom.window.MouseEvent;
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);

const context: ContextInfo = { used: 0, window: 100, sessionTokens: 0 };
const effort: EffortInfo = { supported: true, current: "auto", default: "auto", levels: ["auto"] };
const balance: BalanceInfo = { available: false, display: "" };
const jobs: JobView[] = [];
const checkpoints: CheckpointMeta[] = [];
const history: HistoryMessage[] = [{ role: "user", content: "hello" }, { role: "assistant", content: "hi" }];
const created: string[] = [];
let attachFails = true;
let childSeq = 0;
const pending = new Map<string, { operationId: string; sessionId: string }>();
let forkTargetsRead: () => Promise<{ targets: never[]; verifiable: boolean }> = async () => ({ targets: [], verifiable: true });
const anchor = (turnId: string) => ({ sourceSessionId: "source-1", sessionGeneration: 1, turnId, boundarySequence: 9,
  turnNumber: 1, status: "committed", available: true });

installDesktopHostStub(({
  main: {
    App: {
      ListTabs: async () => [tabMeta("tab-a")],
      MetaForTab: async (tabId: string) => meta(tabId),
      ContextUsageForTab: async () => context,
      EffortForTab: async () => effort,
      BalanceForTab: async () => balance,
      JobsForTab: async () => jobs,
      CheckpointsForTab: async () => checkpoints,
      ForkTargetsForTab: async () => forkTargetsRead(),
      HistoryForTab: async () => history,
      HistoryPageForTab: async (tabId: string) => ({
        messages: history,
        startTurn: 0,
        endTurn: 1,
        totalTurns: 1,
        hasOlder: false,
      }),
      HistorySliceForTab: async (tabId: string, req: HistorySliceRequest) => historySliceFromMessages(tabId, history, req),
      HistoryCheckpointTurnsForTab: async () => [],
      ReplayPendingPrompts: async () => {},
      CreateForkForTab: async (_tabId: string, target: ReturnType<typeof anchor>) => {
        created.push(target.turnId);
        let record = pending.get(target.turnId);
        if (!record) {
          childSeq += 1;
          record = { operationId: `operation-${childSeq}`, sessionId: `child-${childSeq}` };
          pending.set(target.turnId, record);
        }
        if (attachFails) return { ...record, opened: false, error: "conversation fork was created but could not be opened" };
        return { ...record, tabId: "tab-a", opened: true };
      },
      AcknowledgeForkOperation: async (_tabId: string, operationId: string) => {
        for (const [turnId, record] of pending) if (record.operationId === operationId) pending.delete(turnId);
      },
    } as Partial<AppBindings> as AppBindings,
  },
}).main.App);

let resolveLateForkTargets: ((value: { targets: never[]; verifiable: boolean }) => void) | undefined;
forkTargetsRead = () => new Promise((resolve) => { resolveLateForkTargets = resolve; });
const lateForkActions: unknown[] = [];
const forkTargetReads = createForkTargetsRefresh((_tabId, action) => { lateForkActions.push(action); });
const lateForkRead = forkTargetReads.refresh("tab-a");
forkTargetReads.invalidate("tab-a");
resolveLateForkTargets?.({ targets: [], verifiable: true });
await lateForkRead;
ok(lateForkActions.length === 0, "a session rebind invalidates a late fork-target response");
forkTargetsRead = async () => ({ targets: [], verifiable: true });

type Controller = ReturnType<typeof useController>;
let controller: Controller | undefined;
function Probe() { controller = useController(); return null; }
const rootEl = document.getElementById("root");
if (!rootEl) throw new Error("missing root");
const root = createRoot(rootEl);

const notices = () => controller?.state.items.filter((item) => item.kind === "notice").map((item) => (item as { text?: string }).text ?? "") ?? [];

try {
  await act(async () => { root.render(<Probe />); await flushPromises(); });
  await waitFor("the source tab hydrates", () => controller?.activeTabId === "tab-a" && controller.state.hydrating === false);

  await act(async () => { await controller!.forkTurnForTab("tab-a", anchor("turn-2")); await flushPromises(); });
  ok(created.length === 1, "the first fork creates one child");
  ok(notices().some((text) => text.includes("child-1")), "a child whose tab could not open is recovered by name");
  ok(pending.get("turn-2")?.sessionId === "child-1", "the host retains the completed operation until adoption");

  const beforeRepeat = notices().length;
  await act(async () => { await controller!.forkTurnForTab("tab-a", anchor("turn-2")); await flushPromises(); });
  ok(childSeq === 1, "retrying an unacknowledged operation never creates a second child");
  ok(pending.get("turn-2")?.sessionId === "child-1", "the durable operation survives the repeated request");
  ok(notices().slice(beforeRepeat).some((text) => text.includes("child-1")), "the repeat reports the same child instead of creating another");

  await act(async () => { await controller!.forkTurnForTab("tab-a", anchor("turn-7")); await flushPromises(); });
  ok(childSeq === 2, "a fork of another turn creates its own child");
  ok(pending.get("turn-7")?.sessionId === "child-2", "each anchor keeps its own durable operation");

  // An opened child is acknowledged, and its turn stays free for a later
  // intentional fork.
  attachFails = false;
  await act(async () => { await controller!.forkTurnForTab("tab-a", anchor("turn-9")); await flushPromises(); });
  ok(childSeq === 3, "an unrelated turn creates normally");
  ok(pending.get("turn-9") === undefined, "adopting a child acknowledges and clears its operation");

  await act(async () => { await controller!.forkTurnForTab("tab-a", anchor("turn-9")); await flushPromises(); });
  ok(childSeq === 4, "a new click after acknowledgement creates a second intentional child");

} finally {
  await act(async () => { root.unmount(); });
  dom.window.close();
}

console.log(`\n${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
