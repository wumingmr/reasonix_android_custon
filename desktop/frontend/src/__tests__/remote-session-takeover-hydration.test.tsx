// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/remote-session-takeover-hydration.test.tsx
// useRemoteSession's hydration has exactly one legitimate fallback: a session
// a local runtime on the serve host took over answers the Follow request
// with 409 and is read through the legacy history view. Every other failure
// keeps its error, and ownership returning must re-run the real hydration so
// the composer can send again.
import React, { act } from "react";
import { JSDOM } from "jsdom";
import type { AppBindings } from "../lib/bridge";
import type { FollowRequest, TranscriptFollowResponse } from "../generated/desktopContract.generated";
import type { TabMeta } from "../lib/types";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0, failed = 0;
function ok(value: boolean, label: string) {
  process.stdout.write(`  ${value ? "PASS" : "FAIL"}  ${label}\n`);
  if (value) passed += 1; else failed += 1;
}
console.log("\nRemote session take-over hydration");
const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { pretendToBeVisual: true, url: "http://localhost/" });
(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.window = dom.window as unknown as Window & typeof globalThis;
globalThis.document = dom.window.document;
Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
globalThis.localStorage = dom.window.localStorage;
globalThis.requestAnimationFrame = dom.window.requestAnimationFrame?.bind(dom.window) ?? ((cb: FrameRequestCallback) => setTimeout(() => cb(Date.now()), 16) as unknown as number);
globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame?.bind(dom.window) ?? ((handle: number) => clearTimeout(handle));
// The spectator reconcile loop is the hook's only status traffic while taken
// over; capture its callback so the poll path is driven deterministically.
const polls: Array<() => void> = [];
const realSetInterval = dom.window.setInterval.bind(dom.window);
dom.window.setInterval = ((handler: TimerHandler, timeout?: number) => {
  if (typeof handler === "function") polls.push(handler as () => void);
  return realSetInterval(() => {}, timeout ?? 0);
}) as typeof dom.window.setInterval;

const tape: string[] = [];
type FollowMode = "ok" | "transient" | "takeover";
const followMode = new Map<string, FollowMode>();
const takenOver = new Map<string, boolean>();
const statusFor = (tabId: string) => ({ running: false, label: "Model", plan: false, toolApprovalMode: "ask", goal: "", takenOver: takenOver.get(tabId) === true });
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
const desktopStub = installDesktopHostStub({
  async RegisterNavigationIntent() {},
  async SetActiveTab() {},
  async ForkTargetsRemoteTab() { return { targets: [], verifiable: false }; },
  async RemoteTabStatus(tabId: string) { tape.push(`status:${tabId}`); return statusFor(tabId); },
  async RemoteTabMetadata(tabId: string) { tape.push(`metadata:${tabId}`); return { status: statusFor(tabId) }; },
  async RemoteTabSnapshot(tabId: string) { tape.push(`snapshot:${tabId}`); return { history: [{ role: "assistant", content: "legacy history" }], status: statusFor(tabId) }; },
  async RemoteTranscriptFollowForTab(tabId: string, request: FollowRequest) {
    if (request.close) return { protocolVersion: 2, subscription: request.subscription ?? "", changes: [], resetRequired: false };
    if (request.subscription) return new Promise<TranscriptFollowResponse>(() => {});
    tape.push(`follow:${tabId}`);
    const mode = followMode.get(tabId) ?? "ok";
    if (mode === "transient") throw new Error("fetch failed");
    if (mode === "takeover") throw new Error("remote transcript read failed (HTTP 409)");
    return followResponse(tabId);
  },
  // The prime's legacy window read is unavailable before activation on a
  // spectator; keep it out of the picture so the cases isolate hydrate().
  async RemoteSessionHistoryWindowForTab() { return new Promise(() => {}); },
} as Partial<AppBindings> as AppBindings);

const [{ createRoot }, { useRemoteSession }, { setTranscriptBindingIdentity }] = await Promise.all([
  import("react-dom/client"), import("../lib/useRemoteSession"), import("../lib/canonicalTranscriptBackend"),
]);
setTranscriptBindingIdentity(() => "remote");

async function flush(ticks = 4) {
  for (let i = 0; i < ticks; i++) await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 30));
}
let probe: RemoteSessionApi | undefined;
function Probe({ tabId }: { tabId: string }) {
  probe = useRemoteSession(tabId, "ready", `/${tabId}`);
  return null;
}
const root = createRoot(document.getElementById("root")!);
const mount = (tabId: string) => act(async () => { root.render(<Probe tabId={tabId} />); await flush(); });
const count = (entry: string) => tape.filter((item) => item === entry).length;
const texts = () => (probe?.transcript.items ?? []).flatMap((item) => item.kind === "assistant" ? [item.text] : []);
const meta = (id: string, spectator: boolean): TabMeta => ({ id, scope: "project", workspaceRoot: "~/app", workspaceName: "app", topicId: "", topicTitle: "app", label: "box",
  ready: true, running: false, mode: "normal", active: true, cwd: "~/app", sessionGeneration: 1, remote: { hostId: "box", workspace: "~/app" },
  readOnly: spectator, takenOver: spectator } as TabMeta);

// 1. A transient failure is surfaced, not swallowed into the legacy view.
followMode.set("tab-transient", "transient");
await mount("tab-transient");
ok(/fetch failed/.test(probe?.error ?? "") && probe?.hydrated === false, "a transient Follow failure surfaces its error and stays unhydrated");
ok(count("snapshot:tab-transient") === 0 && texts().length === 0, "a transient failure never installs the legacy history view");
ok(count("status:tab-transient") >= 1, "the ownership verdict is read from status before deciding against the fallback");

// 2. A taken-over session (409) falls back to the legacy history view.
followMode.set("tab-spectator", "takeover");
takenOver.set("tab-spectator", true);
await mount("tab-spectator");
ok(probe?.hydrated === true && probe.state === "ready" && probe.error === "", "a 409 take-over hydrates through the legacy fallback");
ok(probe?.transcript.transcriptProtocol !== 2 && texts().includes("legacy history") && count("snapshot:tab-spectator") === 1, "the spectator reads the file-backed history view");
ok(polls.length >= 1, "a spectator arms the slow status reconcile loop");
let submitError = "";
await act(async () => { await probe?.submit("hello").catch((error: unknown) => { submitError = String(error); }); await flush(); });
ok(/not synchronized/.test(submitError), "a spectator cannot submit over the legacy view, so ownership return must repair it");

// 3. Ownership returning through the tab meta re-runs hydration.
followMode.set("tab-spectator", "ok");
takenOver.set("tab-spectator", false);
const followsBefore = count("follow:tab-spectator");
await act(async () => { desktopStub.emit("remote-tab:updated", meta("tab-spectator", false)); await flush(); });
ok(count("follow:tab-spectator") === followsBefore + 1, "the spectator -> owner flip re-attaches the follower");
ok(probe?.transcript.transcriptProtocol === 2 && probe.hydrated && texts().includes("follower answer") && !texts().includes("legacy history"),
  "ownership return replaces the legacy view with the live transcript");
await act(async () => { desktopStub.emit("remote-tab:updated", meta("tab-spectator", false)); await flush(); });
ok(count("follow:tab-spectator") === followsBefore + 1, "a repeated owner publication does not re-hydrate again");

// 4. The status verdict alone gates the fallback when the error text is not a 409.
followMode.set("tab-status-verdict", "transient");
takenOver.set("tab-status-verdict", true);
await mount("tab-status-verdict");
ok(probe?.hydrated === true && probe.transcript.transcriptProtocol !== 2 && texts().includes("legacy history"),
  "status.takenOver admits the legacy fallback for a non-409 failure on a spectated session");

// 5. Ownership returning through a status refresh also re-hydrates.
followMode.set("tab-status-verdict", "ok");
takenOver.set("tab-status-verdict", false);
const verdictFollows = count("follow:tab-status-verdict");
const poll = polls[polls.length - 1];
await act(async () => { poll(); await flush(); });
ok(count("follow:tab-status-verdict") === verdictFollows + 1 && probe?.transcript.transcriptProtocol === 2,
  "a status poll observing ownership return re-attaches the follower");

// 6. Switching tabs resets the ownership observation: no spurious re-hydrate.
followMode.set("tab-fresh", "ok");
await mount("tab-fresh");
const freshFollows = count("follow:tab-fresh");
await act(async () => { await flush(); });
ok(freshFollows === 1 && count("follow:tab-fresh") === 1, "a fresh owner tab mounted after a spectator hydrates exactly once");

await act(async () => { root.unmount(); });
desktopStub.uninstall();
if (failed > 0) { console.error(`\n${failed} check(s) failed`); process.exit(1); }
console.log(`\n${passed} passed, 0 failed`);
process.exit(0);
