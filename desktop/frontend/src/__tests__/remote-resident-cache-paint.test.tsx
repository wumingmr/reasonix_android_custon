// Switching back to a remote session whose transcript is already resident must
// paint it immediately and reconcile in the background — the local session
// switch experience — instead of paying the network round trips behind a
// loading placeholder every time.
import React, { act } from "react";
import { JSDOM } from "jsdom";
import type { AppBindings } from "../lib/bridge";
import type { FollowRequest, TranscriptFollowResponse } from "../generated/desktopContract.generated";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0, failed = 0;
function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1; else failed += 1;
}
console.log("\nRemote session resident-cache paint");
const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame?.bind(dom.window) ?? ((cb: FrameRequestCallback) => setTimeout(() => cb(Date.now()), 16) as unknown as number);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame?.bind(dom.window) ?? ((handle: number) => clearTimeout(handle));

type FollowMode = "ok" | "deferred";
const followMode = new Map<string, FollowMode>();
const deferred = new Map<string, (response: TranscriptFollowResponse) => void>();
const answerFor = new Map<string, string>();
const statusFor = () => ({ running: false, label: "Model", plan: false, toolApprovalMode: "ask", goal: "", takenOver: false });
const inline = (id: string, content: string) => ({ messageId: id, position: 0, version: 1, role: "assistant", eventSequence: 1, visibleTurn: 1, preview: "", inline: { id, role: "assistant", content } });
const followResponse = (tabId: string): TranscriptFollowResponse => ({
  protocolVersion: 2, subscription: `sub-${tabId}`, changes: [], resetRequired: false,
  snapshot: {
    protocolVersion: 1, snapshotId: `cut-${tabId}`, identity: { sessionId: tabId, runtimeEpoch: "epoch", rewriteEpoch: 0, headId: "" },
    projectionRevision: 10, coveredThroughSeq: 4, durableSeq: 4, records: [], activeRecords: [], activeAttempts: [],
    runtime: { status: "completed", pendingEvents: [], samplingCount: 0, toolCount: 0 }, before: 0, hasOlder: false, totalRecords: 1, totalTurns: 1, stale: false,
  },
  history: { status: "ready", snapshotSequence: 4, coverageSequence: 4, generation: "gen", totalTurns: 1, hasOlder: false, hasNewer: false, messages: [inline(`answer-${tabId}`, answerFor.get(tabId) ?? "follower answer")] },
});
installDesktopHostStub({
  async RegisterNavigationIntent() {},
  async SetActiveTab() {},
  async ForkTargetsRemoteTab() { return { targets: [], verifiable: false }; },
  async RemoteTabStatus() { return statusFor(); },
  async RemoteTabMetadata() { return { status: statusFor() }; },
  async RemoteTabSnapshot() { return { history: [], status: statusFor() }; },
  async RemoteTranscriptFollowForTab(tabId: string, request: FollowRequest) {
    if (request.close) return { protocolVersion: 2, subscription: request.subscription ?? "", changes: [], resetRequired: false };
    if (request.subscription) return new Promise<TranscriptFollowResponse>(() => {});
    if ((followMode.get(tabId) ?? "ok") === "deferred") {
      return new Promise<TranscriptFollowResponse>((resolve) => { deferred.set(tabId, resolve); });
    }
    return followResponse(tabId);
  },
  async RemoteSessionHistoryWindowForTab() { return new Promise(() => {}); },
} as Partial<AppBindings> as AppBindings);

const [{ createRoot }, { useRemoteSession }, { getTranscriptStore }, { setTranscriptBindingIdentity }] = await Promise.all([
  import("react-dom/client"), import("../lib/useRemoteSession"), import("../lib/transcriptStore"), import("../lib/canonicalTranscriptBackend"),
]);
setTranscriptBindingIdentity(() => "remote");

async function flush(ticks = 8) {
  for (let i = 0; i < ticks; i++) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 30));
}
let probe: RemoteSessionApi | undefined;
function Probe({ tabId, sessionPath }: { tabId: string; sessionPath: string }) {
  probe = useRemoteSession(tabId, "ready", sessionPath);
  return null;
}
const root = createRoot(document.getElementById("root")!);
const mount = (tabId: string, sessionPath: string) => act(async () => {
  root.render(<Probe tabId={tabId} sessionPath={sessionPath} />);
  await flush();
  // The follower runtime is dynamically imported on first use.
  for (let i = 0; i < 3; i++) await new Promise((resolve) => setTimeout(resolve, 40));
  await flush();
});
const texts = () => (probe?.transcript.items ?? []).flatMap((item) => item.kind === "assistant" ? [item.text] : []);

// 1. Visit session A so its transcript becomes resident, then switch away.
followMode.set("tab-switch", "ok");
answerFor.set("tab-switch", "session A answer");
await mount("tab-switch", "session-a");
// The mount effect's hydrate depends on the follower runtime's dynamic import,
// which settles unreliably in JSDOM (the repo's prime suite is red on HEAD for
// the same reason). Drive it explicitly to reach the state under test.
await act(async () => { await probe?.retryHydration(); await flush(); });
ok(probe?.hydrated === true && texts().includes("session A answer"), "session A hydrates and becomes resident");

// 2. Switching to a session with no resident cut keeps the network path.
followMode.set("tab-switch", "deferred");
await mount("tab-switch", "session-b");
ok(probe?.hydrated === false && probe?.revalidating === false, "an uncached session still waits for its hydrate");

// 3. Switching back paints the resident cut with no network wait.
await mount("tab-switch", "session-a");
ok(texts().includes("session A answer"), "the resident session is visible immediately on switch-back");
ok(probe?.hydrated === true && probe?.revalidating === true, "the painted surface reports revalidating while the hydrate runs");
ok(getTranscriptStore().peek("tab-switch", "session-a") !== undefined, "the paint reads the session-keyed resident cut");

// 4. The background hydrate supersedes the paint.
answerFor.set("tab-switch", "session A revalidated");
await act(async () => { void probe?.retryHydration(); await flush(); });
await act(async () => { deferred.get("tab-switch")?.(followResponse("tab-switch")); await flush(); });
ok(texts().includes("session A revalidated") && !texts().includes("session A answer"), "the authoritative cut replaces the painted one");
ok(probe?.revalidating === false && probe?.hydrated === true, "revalidating clears once the hydrate settles");

await act(async () => { root.unmount(); });
dom.window.close();
console.log(`\n${passed} passed, ${failed} failed`);
process.exit(failed === 0 ? 0 : 1);
