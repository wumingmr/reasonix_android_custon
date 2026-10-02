import assert from "node:assert/strict";
import React, { act, useLayoutEffect, useRef } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useRuntimeEventHandlers } from "../app-runtime/useRuntimeEventHandlers";
import { runtimeStateStore, type RuntimeProjection, type RuntimeSession } from "../lib/runtimeStateStore";
import { ToastProvider } from "../lib/toast";
import { LocaleProvider } from "../lib/i18n";
import { setAttentionPreference, setSuccessPreference } from "../lib/sound";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document,
  localStorage: dom.window.localStorage, IS_REACT_ACT_ENVIRONMENT: true });
let chimes = 0;
// The sound suite verifies synthesis; this probe counts actual playback entries.
Object.assign(globalThis, { AudioContext: class { constructor() { chimes++; } } });
setAttentionPreference("synth");
setSuccessPreference("synth");
installDesktopHostStub({ ListTabs: async () => [] });
let handlers!: ReturnType<typeof useRuntimeEventHandlers>;
function Probe({ active = "B" }: { active?: string }) {
  handlers = useRuntimeEventHandlers({ activeTabId: active, workspaceScopeKey: "fixture",
    workspaceScopeActiveTabRef: useRef(active), userPlanModeByTabRef: useRef({}),
    setTabMetas() {}, setTabOrderIds() {}, setComposerProfilesByTab() {}, setDockRefreshKey() {},
    setProjectRevision() {}, setWorkspaceControllerEpoch() {}, async setControllerCollaborationMode() {} });
  useLayoutEffect(() => {
    // Layout precedes the notification module's passive loading effect.
    handlers.handleRuntimeEvent({ kind: "turn_done", tabId: "B" });
  }, []);
  return <textarea aria-label="draft" defaultValue="B draft" />;
}
const root = createRoot(document.getElementById("root")!);
const paint = (active = "B") => act(async () => root.render(<LocaleProvider><ToastProvider><Probe active={active} /></ToastProvider></LocaleProvider>));
let revision = 0;
function session(kind = "ask", turnId = "turn-A", requestId = "1"): RuntimeSession {
  return { tabId: "detached:A", scope: "project", workspaceRoot: "/fixture", topicId: "topic-A",
    sessionId: "session-A", sessionPath: "", sessionGeneration: 1, open: false, remote: false, freshness: "synced",
    state: { schemaVersion: 1, runtimeEpoch: "runtime-A", activityRevision: 1, revision: 1, phase: "executing",
      running: true, turnId, turnStatus: "running", turnEventSeq: 1, pendingPrompt: true,
      pendingInteractions: [{ requestId, kind, turnId, runtimeEpoch: "runtime-A", headId: "head-A" }],
      cancelRequested: false, cancellable: true, backgroundJobs: 0, activity: "" } };
}
async function publish(sessions: RuntimeSession[]) {
  const snapshot: RuntimeProjection = { epoch: "host", revision: ++revision, sessions,
    topics: [{ scope: "project", workspaceRoot: "/fixture", node: { key: "topic-A", topicId: "topic-A", label: "Conversation A", kind: "session", session: { hostId: "local", sessionId: "session-A" } } }] };
  await act(async () => runtimeStateStore.commit(snapshot));
}
try {
  await paint();
  await act(async () => { await import("../lib/runtimeNotifications"); });
  assert.equal(chimes, 1, "completion arriving before notification code loads is queued, not lost");
  chimes = 0;
  await act(async () => handlers.handleRuntimeEvent({ kind: "turn_done", err: "fixture error" }));
  assert.equal(chimes, 0, "failed completion remains silent");
  await publish([session()]);
  assert.equal(chimes, 1, "detached A asks while B is being edited: sound must play before switching");
  assert.match(document.querySelector('[role="status"]')!.textContent!, /Conversation A.*answer/i);
  assert.equal((document.querySelector("textarea") as HTMLTextAreaElement).value, "B draft");
  await publish([session()]);
  await act(async () => handlers.handleRuntimeReady("A"));
  await act(async () => handlers.handleRuntimeRebuilt("A", "new-view-epoch"));
  await act(async () => handlers.handleRuntimeReady());
  await paint("A");
  await act(async () => handlers.handleRuntimeEvent({ kind: "ask_request", tabId: "A", turnId: "turn-A", ask: { id: "1", questions: [] } }));
  assert.equal(chimes, 1, "reattach, ready and prompt replay must not ring again");
  await paint("B");
  await act(async () => handlers.handleRuntimeEvent({ kind: "approval_request", tabId: "A", turnId: "turn-approval", approval: { id: "1", tool: "bash", subject: "fixture" } }));
  await publish([session("approval", "turn-approval")]);
  assert.equal(chimes, 2, "wire first, snapshot second: approval rings once");
  await publish([session("ask", "turn-new")]);
  assert.equal(chimes, 3, "a new turn may reuse a per-controller request id");
  await publish([{ ...session("ask", "turn-remote"), remote: true, tabId: "B", hostId: "remote" }]);
  assert.equal(chimes, 4, "remote background session sharing the visible tab still notifies");
  await publish([{ ...session("ask", "turn-unknown"), freshness: "unknown" }]);
  assert.equal(chimes, 4, "untrusted stale remote state cannot create a new notification");
  await publish([{ ...session("ask", "turn-unknown"), freshness: "synced" }]);
  assert.equal(chimes, 5, "reconnection can recover a previously unseen pending question");
  setAttentionPreference("off");
  await publish([session("ask", "turn-muted")]);
  assert.equal(chimes, 5, "muted audio stays muted");
  assert.equal(document.querySelectorAll(".toast").length, 6, "visual alerts remain enabled with audio off");
  setAttentionPreference("synth");
  await publish([{ ...session("ask", "turn-visible"), tabId: "B", open: true }]);
  assert.equal(chimes, 6, "visible requests retain their normal prompt sound");
  assert.equal(document.querySelectorAll(".toast").length, 6, "visible prompt cards need no background toast");
  for (const kind of ["plan", "recovery"]) {
    await publish([session(kind, `turn-${kind}`)]);
    await act(async () => handlers.handleRuntimeEvent({ kind: "approval_request", tabId: "A", turnId: `turn-${kind}`,
      approval: { id: "1", tool: "fixture", subject: "fixture", kind } }));
  }
  assert.equal(chimes, 8, "plan and recovery approvals share request/snapshot dedupe");
  await act(async () => root.unmount());
  await publish([session("ask", "turn-disposed")]);
  assert.equal(chimes, 8, "unmount removes the global state subscription");
  console.log("runtime attention: background Ask, approval, replay, navigation, remote, reconnect, mute and disposal passed");
} finally { dom.window.close(); }
