import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";

import { useSessionDraftSurface } from "../app-runtime/useSessionDraftSurface";
import type { SessionDraftView } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => { resolve = next; });
  return { promise, resolve };
}

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  IS_REACT_ACT_ENVIRONMENT: true,
  requestAnimationFrame: (callback: FrameRequestCallback) => setTimeout(() => callback(0), 0),
  cancelAnimationFrame: (id: number) => clearTimeout(id),
});
dom.window.requestAnimationFrame = globalThis.requestAnimationFrame;
dom.window.cancelAnimationFrame = globalThis.cancelAnimationFrame;

const settings = {
  model: "fixture/model",
  mode: "normal",
  toolApprovalMode: "ask",
  disabledMcp: {},
  mcpOrder: [],
};
const restoredDraft: SessionDraftView = {
  id: "draft-restored",
  workspaceId: "workspace-restored",
  scope: "project",
  workspaceRoot: "/workspace/restored",
  revision: 1,
  contentJson: "{}",
  settings,
  status: "active",
  updatedAt: 1,
};
const globalDraft: SessionDraftView = {
  ...restoredDraft,
  id: "draft-global",
  workspaceId: "workspace-global",
  scope: "global",
  workspaceRoot: "",
};

let restore = async (): Promise<SessionDraftView | null> => null;
let tabs: Array<{ id: string }> = [];
let globalOpenCalls = 0;
const restoreTargets: string[] = [];
const stub = installDesktopHostStub({
  ListSessionDraftSummaries: async () => [],
  RestoreSessionDraft: () => restore(),
  ListTabs: async () => tabs,
  OpenSessionDraftForTarget: async (scope: string) => {
    globalOpenCalls++;
    return structuredClone(scope === "project" ? restoredDraft : globalDraft);
  },
  GetDraftContext: async (id: string) => ({
    draft: structuredClone(id === globalDraft.id ? globalDraft : restoredDraft),
    commands: [],
    servers: [],
    models: [],
  }),
  SetSessionDraftRestoreTarget: async (id: string) => { restoreTargets.push(id); },
  DismissSessionDraft: async () => {},
  AttachmentDataURLForTarget: async () => "",
});

let navigationIntent = 0;
let claimCalls = 0;
let owner!: ReturnType<typeof useSessionDraftSurface>;
function Probe() {
  owner = useSessionDraftSurface({
    onAccepted: () => {},
    onChanged: () => {},
    claimNavigationIntent: () => {
      claimCalls++;
      return ++navigationIntent;
    },
    currentNavigationIntent: () => navigationIntent,
    isNavigationIntentCurrent: (intent) => intent === navigationIntent,
  });
  return null;
}

const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => { root.render(<Probe />); });

  const lateRestore = deferred<SessionDraftView | null>();
  restore = () => lateRestore.promise;
  let pending!: Promise<void>;
  act(() => { pending = owner.initializeEmptySurface(); });
  navigationIntent++;
  lateRestore.resolve(structuredClone(restoredDraft));
  await act(async () => { await pending; });
  assert.equal(owner.surface, null, "a late restore read cannot override newer user navigation");
  assert.equal(claimCalls, 0, "a stale startup probe never claims a fresh navigation intent");

  restore = async () => null;
  tabs = [{ id: "formal" }];
  await act(async () => { await owner.initializeEmptySurface(); });
  assert.equal(owner.surface, null, "an existing formal session remains the visible startup surface");
  assert.equal(claimCalls, 0, "probing startup state does not invalidate formal-session hydration");
  assert.equal(globalOpenCalls, 0, "an existing formal session does not create a global draft");

  restore = async () => structuredClone(restoredDraft);
  await act(async () => { await owner.initializeEmptySurface(); });
  assert.equal(owner.surface, null, "a saved legacy draft must not replace normal startup navigation");
  assert.equal(claimCalls, 0, "legacy recovery is explicit and never claims startup navigation");
  assert.equal(restoreTargets.length, 0, "startup must not overwrite the legacy restore marker");

  await act(async () => { await owner.dismiss(); });
  restore = async () => null;
  tabs = [];
  await act(async () => { await owner.initializeEmptySurface(); });
  assert.equal(owner.surface, null, "startup without a formal session keeps the welcome page empty");
  assert.equal(claimCalls, 0, "empty startup must not claim a draft navigation intent");
  assert.equal(globalOpenCalls, 0, "empty startup must not create a new legacy draft");

  await act(async () => { await owner.open(restoredDraft.scope, restoredDraft.workspaceRoot); });
  assert.equal(owner.surface?.draft.id, restoredDraft.id, "the explicit recovery entry still opens the original draft");
  assert.equal(claimCalls, 1, "only explicit recovery claims navigation");
  assert.equal(restoreTargets.at(-1), restoredDraft.id);

  await act(async () => { root.unmount(); });
  console.log("session draft startup navigation: no implicit creation or startup takeover; explicit recovery retained");
} finally {
  stub.uninstall();
  dom.window.close();
}
