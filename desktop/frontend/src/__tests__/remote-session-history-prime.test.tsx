// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/remote-session-history-prime.test.tsx
// The pre-activation history prime in useRemoteSession publishes a durable
// baseline for a blank remote tab, but the follower owns the resident store
// once it has published. These cases pin the ordering: a late legacy window
// can neither replace follower records nor bump the store generation, retries
// after ownership are no-ops, and a transcript that already has content is
// never primed.
import React, { act } from "react";
import { JSDOM } from "jsdom";
import { mock } from "node:test";
import type { AppBindings } from "../lib/bridge";
import type { FollowRequest, HistoryWindowPage, TranscriptFollowResponse } from "../generated/desktopContract.generated";
import type { TabMeta } from "../lib/types";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0, failed = 0;
function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1; else failed += 1;
}
console.log("\nRemote session early history prime ownership");
const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame?.bind(dom.window) ?? ((cb: FrameRequestCallback) => setTimeout(() => cb(Date.now()), 16) as unknown as number);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame?.bind(dom.window) ?? ((handle: number) => clearTimeout(handle));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((r, j) => { resolve = r; reject = j; });
  return { promise, resolve, reject };
}
const tape: string[] = [];
const followMode = new Map<string, "ok" | "deferred">();
const followDeferred = new Map<string, ReturnType<typeof deferred<TranscriptFollowResponse>>>();
const polls = new Map<string, ReturnType<typeof deferred<TranscriptFollowResponse>>>();
const windowDeferred = new Map<string, ReturnType<typeof deferred<HistoryWindowPage>>>();
const status = { running: false, label: "Model", plan: false, toolApprovalMode: "ask", goal: "" };
const inline = (id: string, content: string) => ({ messageId: id, position: 0, version: 1, role: "assistant", eventSequence: 1, visibleTurn: 1, preview: "", inline: { id, role: "assistant", content } });
function followResponse(tabId: string): TranscriptFollowResponse {
  return {
    protocolVersion: 2, subscription: `sub-${tabId}`, changes: [], resetRequired: false,
    snapshot: {
      protocolVersion: 1, snapshotId: `cut-${tabId}`, identity: { sessionId: tabId, runtimeEpoch: "epoch", rewriteEpoch: 0, headId: "" },
      projectionRevision: 10, coveredThroughSeq: 4, durableSeq: 4, records: [], activeRecords: [], activeAttempts: [],
      runtime: { status: "completed", pendingEvents: [], samplingCount: 0, toolCount: 0 }, before: 0, hasOlder: false, totalRecords: 1, totalTurns: 1, stale: false,
    },
    history: { status: "ready", snapshotSequence: 4, coverageSequence: 4, generation: "gen", totalTurns: 1, hasOlder: false, hasNewer: false, messages: [inline("follower-answer", "follower answer")] },
  };
}
const windowPage = (): HistoryWindowPage => ({ messages: [inline("primed-answer", "primed history")], status: "ready", snapshotSequence: 2, coverageSequence: 2, generation: "legacy", totalTurns: 1, hasOlder: false, hasNewer: false, olderCursor: "", newerCursor: "" });
const desktopStub = installDesktopHostStub({
  async RegisterNavigationIntent() {},
  async SetActiveTab() {},
  async ForkTargetsRemoteTab() { return { targets: [], verifiable: false }; },
  async RemoteTabStatus(tabId: string) { tape.push(`status:${tabId}`); return status; },
  async RemoteTabMetadata(tabId: string) { tape.push(`metadata:${tabId}`); return { status }; },
  async RemoteTabSnapshot(tabId: string) { tape.push(`snapshot:${tabId}`); return { history: [], status }; },
  async RemoteTranscriptFollowForTab(tabId: string, request: FollowRequest) {
    if (request.close) { tape.push(`close:${tabId}`); return { protocolVersion: 2, subscription: request.subscription ?? "", changes: [], resetRequired: false }; }
    if (request.subscription) { const poll = deferred<TranscriptFollowResponse>(); polls.set(tabId, poll); return poll.promise; }
    tape.push(`follow:${tabId}`);
    if (followMode.get(tabId) === "deferred") {
      const pending = deferred<TranscriptFollowResponse>();
      followDeferred.set(tabId, pending);
      return pending.promise;
    }
    return followResponse(tabId);
  },
  async RemoteSessionHistoryWindowForTab(tabId: string) {
    tape.push(`window:${tabId}`);
    const pending = deferred<HistoryWindowPage>();
    windowDeferred.set(tabId, pending);
    return pending.promise;
  },
} as Partial<AppBindings> as AppBindings);

const [{ createRoot }, { useRemoteSession }, { getTranscriptStore }, { initialState }, { setTranscriptBindingIdentity }] = await Promise.all([
  import("react-dom/client"), import("../lib/useRemoteSession"), import("../lib/transcriptStore"), import("../lib/useController"),
  import("../lib/canonicalTranscriptBackend"),
]);
// Production resolves remote tabs through the controller's meta; this harness
// has no controller, so bind the canonical reads to the remote bridge directly.
setTranscriptBindingIdentity(() => "remote");

async function flush(ticks = 4) {
  for (let i = 0; i < ticks; i++) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 30));
}
let probe: RemoteSessionApi | undefined;
function Probe({ tabId, path }: { tabId: string; path: string }) {
  probe = useRemoteSession(tabId, "ready", path);
  return null;
}
const root = createRoot(document.getElementById("root")!);
const mount = (tabId: string, path: string) => act(async () => { root.render(<Probe tabId={tabId} path={path} />); await flush(); });
const count = (entry: string) => tape.filter((item) => item === entry).length;
const texts = () => (probe?.transcript.items ?? []).flatMap((item) => item.kind === "assistant" ? [item.text] : []);
const meta = (id: string): TabMeta => ({ id, scope: "project", workspaceRoot: "~/app", workspaceName: "app", topicId: "", topicTitle: "app", label: "box",
  ready: true, running: false, mode: "normal", active: true, cwd: "~/app", sessionGeneration: 1, remote: { hostId: "box", workspace: "~/app" } } as TabMeta);

// 1. Follower publishes first; the prime's window resolves later.
followMode.set("tab-late-window", "ok");
await mount("tab-late-window", "/late-window");
ok(texts().includes("follower answer") && probe?.transcript.transcriptProtocol === 2, "the follower installs its cut while the legacy window read is still in flight");
const generationAfterFollower = getTranscriptStore().generationOf("tab-late-window", "/late-window");
await act(async () => { windowDeferred.get("tab-late-window")?.resolve(windowPage()); await flush(); });
ok(!texts().includes("primed history") && texts().includes("follower answer"), "a legacy window landing after the follower cut does not replace the transcript");
ok(!getTranscriptStore().peek("tab-late-window", "/late-window")?.items.some((item) => item.kind === "assistant" && item.text === "primed history"),
  "a late legacy window does not overwrite follower records in the resident store");
const windowReadsBefore = count("window:tab-late-window");
await act(async () => { desktopStub.emit("remote-tab:updated", meta("tab-late-window")); await flush(); });
ok(count("window:tab-late-window") === windowReadsBefore, "attach publications after follower ownership do not re-read the legacy window");
ok(getTranscriptStore().generationOf("tab-late-window", "/late-window") === generationAfterFollower, "retired prime attempts leave the resident session generation alone");

// 2. Existing content is never primed, so the store is not even touched.
getTranscriptStore().setState("tab-resident", { ...initialState, items: [{ kind: "assistant", id: "m:seed", text: "resident answer", reasoning: "", streaming: false }] });
followMode.set("tab-resident", "deferred");
await mount("tab-resident", "/resident");
ok(count("window:tab-resident") === 0, "a transcript that already has content skips the legacy window read entirely");
ok(getTranscriptStore().generationOf("tab-resident", "/resident") === undefined, "skipping the prime creates no resident session and bumps no generation");
ok(texts().includes("resident answer"), "resident content stays visible while the follower is still connecting");

// 3. A blank tab whose follower is slow still gets the durable baseline, and the follower supersedes it.
followMode.set("tab-prime-first", "deferred");
await mount("tab-prime-first", "/prime-first");
await act(async () => { windowDeferred.get("tab-prime-first")?.resolve(windowPage()); await flush(); });
ok(texts().includes("primed history") && probe?.transcript.transcriptProtocol !== 2, "a blank tab is primed from the legacy window before the follower connects");
await act(async () => { followDeferred.get("tab-prime-first")?.resolve(followResponse("tab-prime-first")); await flush(); });
ok(texts().includes("follower answer") && !texts().includes("primed history") && probe?.transcript.transcriptProtocol === 2, "the follower cut supersedes the primed baseline");
const primeReads = count("window:tab-prime-first");
await act(async () => { desktopStub.emit("remote-tab:updated", meta("tab-prime-first")); await flush(); });
ok(count("window:tab-prime-first") === primeReads, "a primed-then-followed tab does not read the legacy window again");

// 4. The remote hook uses the same shell-stop fence as local followers.
await mount("tab-stopping", "/stopping");
const readsBeforeStop = count("follow:tab-stopping"), closesBeforeStop = count("close:tab-stopping");
const retainedItems = probe!.transcript.items;
mock.timers.enable({ apis: ["setTimeout"] });
try {
  await act(async () => {
    desktopStub.emitServiceState({ phase: "stopping", generation: "test-service" });
    polls.get("tab-stopping")!.reject(new Error("desktop service is shutting down"));
    for (let i = 0; i < 30; i++) await Promise.resolve();
    mock.timers.tick(1000);
    for (let i = 0; i < 30; i++) await Promise.resolve();
  });
  ok(count("follow:tab-stopping") === readsBeforeStop, "remote shutdown does not retry baseline reads");
  ok(count("close:tab-stopping") === closesBeforeStop, "remote shutdown sends no subscription cleanup RPC");
  ok(probe!.transcript.items === retainedItems, "remote shutdown retains the displayed transcript");
} finally { mock.timers.reset(); }

await act(async () => { root.unmount(); });
desktopStub.uninstall();
if (failed > 0) { console.error(`\n${failed} check(s) failed`); process.exit(1); }
console.log(`\n${passed} passed, 0 failed`);
process.exit(0);
