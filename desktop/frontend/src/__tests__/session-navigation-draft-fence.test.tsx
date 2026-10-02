import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import type { ManualSessionCreationView, ManualSessionCreationRequest } from "../generated/desktopContract.generated";

import { useSessionNavigationCommands, type SessionNavigationCommandsInput } from "../app-runtime/useSessionNavigationCommands";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

const dom = new JSDOM("<div id='root'></div>");
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  IS_REACT_ACT_ENVIRONMENT: true,
});

let intent = 0;
let commands!: ReturnType<typeof useSessionNavigationCommands>;
const creationStarted = deferred();
const finishCreation = deferred();
let blockCreation = false;
const created: string[] = [];
const legacyCalls: string[] = [];
const begun: number[] = [];
const notices: string[] = [];
const retired = (name: string) => async () => { legacyCalls.push(name); throw new Error("retired draft database is unreadable"); };
const stub = installDesktopHostStub({
  ...makeSessionUIMock(async (_scope,_root,id) => {
    created.push(id);
    if (blockCreation) { creationStarted.resolve(); await finishCreation.promise; }
  }),
  ListSessionDraftSummaries: retired("list"), OpenSessionDraftForTarget: retired("open"),
  DismissSessionDraft: retired("dismiss"), SetSessionDraftRestoreTarget: retired("restore"),
});
const enqueued: Array<{ request: unknown; intent: number }> = [];

function Probe() {
  commands = useSessionNavigationCommands({
    activeTab: { id: "fixture", scope: "project", workspaceRoot: "/workspace" },
    showToast: message => { notices.push(message); },
    closeTransientOverlays: () => {},
    clearImDetail: () => {},
    prepareBlankWorkspace: () => {},
    navigation: {
      enqueueNavigation: async () => {},
      enqueueNavigationWithIntent: async (request, navigationIntent) => {
        enqueued.push({ request, intent: navigationIntent });
      },
      openRemoteProject: async () => ({ status: "cancelled", reason: "superseded" }),
    },
    noteNavigationIntent: () => ++intent,
    beginNavigationSurface: seq => { begun.push(seq); },
    settleNavigationSurface: () => {},
    isNavigationIntentCurrent: (candidate) => candidate === intent,
    markProjectChanged: () => {},
    refreshTabMetas: async () => {},
    refreshHistoryView: () => {},
    enterConversation: () => {},
    pickWorkspace: async () => "",
    switchWorkspace: async () => {},
    ports: {
      openTaskSessionForTab: async () => ({ ok: false }),
      listSessionsForTab: async () => [],
    },
  } as SessionNavigationCommandsInput);
  return null;
}

const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => { root.render(<Probe />); });
  await act(async () => { await commands.handleOpenTopic("project", "/workspace", "existing-topic", "session-id:existing-session"); });
  assert.deepEqual(enqueued.at(-1)?.request, {
    kind: "canonical-session", ref: { hostId: "local", sessionId: "existing-session" },
  }, "sidebar session keeps its exact local identity on the canonical session path");
  enqueued.length = 0;
  intent = 0;
  await act(async () => {
    await commands.openBlankSession("global", "/ignored/global/root");
    await commands.openBlankSession("project", "/workspace");
    await commands.handleNewTab();
  });
  assert.deepEqual(legacyCalls, [], "manual new never touches retired input, even if its database is unreadable");
  assert.equal(new Set(created).size, 3, "each click has its own formal identity");
  assert.equal(enqueued.length,3);
  enqueued.length=0;
  blockCreation = true;

  let stale!: Promise<void>;
  act(() => {
    stale = commands.openBlankSession("project", "/workspace");
  });
  await act(async () => { await creationStarted.promise; });
  await act(async () => { await commands.openCanonicalSession({ hostId: "local", sessionId: "session-b" }); });
  finishCreation.resolve();
  await act(async () => { await stale; });
  assert.equal(enqueued.length, 1);
  assert.equal(enqueued[0]?.intent, 5, "late formal creation cannot override newer navigation");
  assert.deepEqual(enqueued[0]?.request, {
    kind: "canonical-session",
    ref: { hostId: "local", sessionId: "session-b" },
  });
  assert.deepEqual(legacyCalls, [], "navigation never waits for or writes old drafts");

  enqueued.length = 0;
  const pollStarted = deferred();
  let complete!: (view: ManualSessionCreationView) => void;
  const pending = new Promise<ManualSessionCreationView>(resolve => { complete = resolve; });
  let starting!: ManualSessionCreationView;
  stub.commands.BeginManualSessionCreation = async (request: ManualSessionCreationRequest) => {
    starting = { operationId: request.operationId, workspaceId: "canonical", scope: "project", workspaceRoot: "/workspace",
      ref: { hostId: "local", sessionId: "independent-new-session" }, topicId: "topic", phase: "starting", surfaceReady: true,
      settings: { model: "stub", mode: "normal", toolApprovalMode: "default", disabledMcp: {}, mcpOrder: [] } };
    return starting;
  };
  stub.commands.GetManualSessionCreation = async (operationId: string) => {
    assert.equal(operationId, starting.operationId, "observation retains the original creation identity");
    pollStarted.resolve(); return pending;
  };
  let creating!: Promise<void>;
  act(() => { creating = commands.handleNewTab(); });
  assert.equal(begun.at(-1), 6, "new click fences the old composer before the first asynchronous result");
  await act(async () => { await pollStarted.promise; });
  assert.deepEqual(enqueued[0]?.request, { kind: "canonical-session", ref: starting.ref }, "new input surface opens before runtime readiness");
  await act(async () => { await commands.openCanonicalSession({ hostId: "local", sessionId: "session-c" }); });
  complete({ ...starting, phase: "ready" });
  await act(async () => { await creating; });
  assert.equal(enqueued.length, 2, "runtime completion never reselects the new session after the user leaves");
  assert.deepEqual(enqueued.at(-1)?.request, { kind: "canonical-session", ref: { hostId: "local", sessionId: "session-c" } });
  assert.deepEqual(notices, []);

  const failedRequests: string[] = [];
  stub.commands.BeginManualSessionCreation = async (request: ManualSessionCreationRequest) => {
    failedRequests.push(request.operationId);
    throw new Error("transport unavailable");
  };
  stub.commands.GetManualSessionCreation = async () => { throw new Error("still unavailable"); };
  await act(async () => { await commands.handleNewTab(); });
  assert.equal(commands.manualCreation?.failed, true, "unacknowledged creation has an inline retry state");
  assert.equal(commands.manualCreation?.pending, false);
  await act(async () => { await commands.retryCreation(); });
  assert.equal(failedRequests.length, 2);
  assert.equal(failedRequests[0], failedRequests[1], "retrying before acknowledgement does not allocate a new operation");
  assert.deepEqual(notices, [], "inline failure is not duplicated in a toast");

  await act(async () => { root.unmount(); });
  console.log("session navigation: retired drafts are untouched; late formal creation preserves the newest selection");
} finally {
  dom.window.close();
}
