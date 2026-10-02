import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { ManualSessionRecovery } from "../components/ManualSessionRecovery";
import { LocaleProvider } from "../lib/i18n";
import { manualCreationPresentation } from "../lib/manualCreationPresentation";
import type { ManualSessionCreationView } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

const item = { operationId: "recovery-test", scope: "global", phase: "starting", ref: { hostId: "local", sessionId: "current" } } as ManualSessionCreationView;
const progress = (status: string, slow = false) => ({ status, stage: "building_runtime", stageStartedAt: 1, elapsedMs: 0, slow });
assert.equal(manualCreationPresentation(item).key, "creation.preparing");
assert.equal(manualCreationPresentation({ ...item, progress: progress("future-status") }).key, "creation.preparing");
assert.equal(manualCreationPresentation({ ...item, progress: progress("running", true) }).key, "creation.slow");
for (const status of ["queued", "running", "waiting_lock", "retrying_storage"]) {
  assert.deepEqual(manualCreationPresentation({ ...item, phase: "failed", progress: progress(status) }), { key: "creation.preparing" }, "host-owned recovery exposes no user decision");
}
assert.deepEqual(manualCreationPresentation({ ...item, progress: progress("waiting_workspace") }), { key: "creation.workspaceUnavailable" });
for (const code of ["workspace_removed", "workspace_changed", "creation_cancelled", "creation_owner_conflict"]) {
  assert.equal(manualCreationPresentation({ ...item, phase: "failed", error: `session_operation:${code}:private detail` }).action, code.startsWith("workspace_") ? "project" : "new");
  assert.equal(manualCreationPresentation({ ...item, phase: "failed", error: `session_operation:${code}:private detail`, progress: progress("queued") }).key, "creation.preparing", "active recovery progress supersedes an earlier failure");
}

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
let retryCount = 0;
let exports = 0;
let finish!: (view: ManualSessionCreationView) => void;
const reply = new Promise<ManualSessionCreationView>(resolve => { finish = resolve; });
const stub = installDesktopHostStub({
  ListManualSessionCreations: async () => [{ ...item, progress: progress("waiting_lock") },
    { ...item, operationId: "old", ref: { hostId: "local", sessionId: "other" }, workspaceRoot: "/private/old-project", phase: "failed" }],
  RetryManualSessionCreation: async () => { retryCount++; return reply; },
  ExportManualCreationDiagnostics: async () => { exports++; return "report.json"; },
});
const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery sessionId="current" /></LocaleProvider>));
  assert.doesNotMatch(document.body.textContent!, /\/private\/old-project|global/, "background creation failures and internal scope labels never appear in the current composer");
  assert.match(document.body.textContent!, /You can type now/);
  assert.equal(document.querySelectorAll("button").length, 0, "ordinary waiting has no decisions or diagnostics");
  await act(async () => root.render(null));
  stub.commands.ListManualSessionCreations = async () => [{ ...item, phase: "failed", error: "session_operation:target_changed: /private/writer", progress: progress("blocked") }];
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery sessionId="current" /></LocaleProvider>));
  assert.doesNotMatch(document.body.textContent!, /session_operation|\/private\/writer/, "internal failures stay out of the normal creation UI");
  const retry = document.querySelector("button")!;
  assert.equal(retry.textContent, "Try again");
  assert.equal(document.querySelector("details")?.open, false);
  await act(async () => [...document.querySelectorAll("button")].find(button => button.textContent === "Export creation diagnostics")!.click());
  assert.equal(exports, 1);
  act(() => { retry.click(); retry.click(); });
  assert.equal(retryCount, 1, "duplicate retry is suppressed while the RPC is pending");
  assert.equal(retry.disabled, true);
  await act(async () => finish({ ...item, progress: progress("running") }));
  assert.match(document.body.textContent!, /You can type now/);
  assert.equal(document.querySelectorAll("button").length, 0);

  await act(async () => root.render(null));
  let lists = 0;
  stub.commands.ListManualSessionCreations = async () => { lists++; return []; };
  const attempt = { request: { operationId: item.operationId, workspaceId: "", scope: "global", workspaceRoot: "" },
    operation: { ...item, surfaceReady: true, progress: progress("running") }, pending: true, failed: false };
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery attempt={attempt} /></LocaleProvider>));
  assert.equal(lists, 0, "an explicit creation reuses its existing observer, without a catalog poll");
  assert.match(document.body.textContent!, /You can type now/);
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery attempt={{ ...attempt, operation: { ...item, phase: "ready" } }} /></LocaleProvider>));
  assert.equal(document.body.textContent, "", "readiness removes the notice");

  let newCount = 0;
  const terminal = { ...attempt, pending: false, operation: { ...item, phase: "failed", error: "session_operation:creation_cancelled:private" } };
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery attempt={terminal} onNew={async () => { newCount++; }} /></LocaleProvider>));
  assert.equal(document.querySelector("button")?.textContent, "New session");
  await act(async () => document.querySelector("button")!.click());
  assert.equal(newCount, 1);
  await act(async () => [...document.querySelectorAll("button")].find(button => button.textContent === "Close")!.click());
  assert.equal(document.body.textContent, "", "dismissing the notice does not mutate or cancel its operation");
  assert.equal(retryCount, 1);

  await act(async () => root.render(null));
  let projects = 0;
  const moved = { ...terminal, operation: { ...item, phase: "failed", error: "session_operation:workspace_changed:private" } };
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery attempt={moved} onChooseProject={() => { projects++; }} /></LocaleProvider>));
  assert.equal(document.querySelector("button")?.textContent, "Choose project");
  await act(async () => document.querySelector("button")!.click());
  assert.equal(projects, 1);

  await act(async () => root.render(null));
  const blocked = { ...item, phase: "failed", progress: progress("blocked") };
  stub.commands.ListManualSessionCreations = async () => [blocked];
  let pollStarted!: () => void;
  const polling = new Promise<void>(resolve => { pollStarted = resolve; });
  let releasePoll!: (view: ManualSessionCreationView) => void;
  stub.commands.GetManualSessionCreation = () => { pollStarted(); return new Promise(resolve => { releasePoll = resolve; }); };
  stub.commands.RetryManualSessionCreation = async () => ({ ...item, progress: progress("running") });
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery sessionId="current" /></LocaleProvider>));
  await act(async () => { await polling; });
  await act(async () => document.querySelector("button")!.click());
  assert.match(document.body.textContent!, /You can type now/);
  await act(async () => releasePoll(blocked));
  assert.match(document.body.textContent!, /You can type now/, "an earlier blocked read cannot overwrite an accepted retry");

  await act(async () => root.render(null));
  let releaseOld!: (items: ManualSessionCreationView[]) => void;
  stub.commands.ListManualSessionCreations = () => new Promise(resolve => { releaseOld = resolve; });
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery key="old-navigation" sessionId="current" /></LocaleProvider>));
  await act(async () => root.render(<LocaleProvider><ManualSessionRecovery key="new-navigation" attempt={{ ...attempt, operation: { ...item, phase: "ready" } }} /></LocaleProvider>));
  await act(async () => releaseOld([{ ...item, phase: "failed" }]));
  assert.equal(document.body.textContent, "", "a late failure from the previous selection cannot reappear");
  console.log("PASS selected-session notices, one observer, coalesced retry, terminal action, dismissal and optional diagnostics");
} finally {
  await act(async () => root.unmount());
  stub.uninstall();
  dom.window.close();
}
