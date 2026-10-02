import assert from "node:assert/strict";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { WorkspaceDockRegion } from "../app-shell/WorkspaceDockRegion";
import type { Translator } from "../lib/i18n";
import { projectConversation, projectConversationLayout, projectNavigationSurfaceTarget } from "../app-runtime/conversationProjection";
import { initialState } from "../lib/useController";
import type { RemoteSessionApi } from "../lib/useRemoteSession";
import type { BackgroundRuntimeView } from "../lib/types";

const local = { ...initialState, running: true, activeTurnId: "local-turn", turnTokens: 999,
  turnStartAt: 111, sessionTokens: 999, sessionCost: 999, sessionCurrency: "USD",
  context: { used: 999, window: 999, sessionTokens: 999 }, balance: { available: true, display: "LOCAL" },
  meta: { ready: true, eventChannel: "local-events", cwd: "local-cwd", label: "local-model", workspaceName: "local-project", gitBranch: "local-branch",
    imageInputEnabled: true, visionFallbackEnabled: true, pinnedFiles: [{ path: "local-file", sizeBytes: 10, tokenEstimate: 5 }] },
} as typeof initialState;
const remote: Pick<RemoteSessionApi, "transcript" | "running" | "modelLabel" | "commands" | "composerProfile" | "goalView" | "goalRuntime" | "effort"> = {
  transcript: { ...initialState, turnTokens: 7, turnStartAt: 222, sessionTokens: 8, sessionCost: 2, sessionCurrency: "CNY" },
  running: false, modelLabel: "remote-model", commands: [],
  goalView: { id: "goal-remote", revision: 3, objective: "resume remote work", phase: "active", maxGoalRounds: null,
    roundsStarted: 7, createdAt: "2026-09-13T10:00:00Z", updatedAt: "2026-09-13T10:10:00Z", activation: "disarmed" },
};
const tab = { id: "remote", label: "remote-tab", remote: { hostId: "fixture", workspace: "remote-cwd" }, workspaceName: "remote-project" };
const background = [{ id: "local-runtime" }] as unknown as BackgroundRuntimeView[];
const view = projectConversation({ local, remote, tab, activeTabId: tab.id, backgroundRuntimes: background, connectingLabel: "connecting" });
assert.equal(view.runtime, remote.transcript, "projection shares the canonical state and message arrays");
assert.equal(view.context.items, remote.transcript.items);
assert.equal(view.context.tabId, tab.id, "remote context reads the selected remote tab");
assert.equal(view.composer.modelLabel, "remote-model");
assert.equal(view.composer.cwd, "remote-cwd");
assert.equal(view.composer.turnTokens, 7);
assert.equal(view.composer.turnStartAt, 222);
assert.equal(view.composer.goalView?.id, "goal-remote");
assert.equal(view.composer.currency, "CNY");
assert.equal(view.composer.pinnedFiles, undefined);
assert.equal(view.composer.attachmentInputEnabled, false);
assert.equal(view.composer.imageInputEnabled, false);
assert.equal(view.composer.imageUnderstandingEnabled, false);
assert.equal(view.composer.localDurableGuidance, false);
assert.equal(view.composer.turnId, undefined);
assert.equal(view.composer.context, remote.transcript.context);
assert.equal(view.composer.balance, remote.transcript.balance);
assert.equal(view.status.context, remote.transcript.context);
assert.equal(view.status.balance, remote.transcript.balance);
assert.deepEqual(view.status.backgroundRuntimes, []);
assert.equal(view.status.gitBranch, undefined);
assert.equal(view.status.workspaceName, "remote-project");
assert.equal(view.status.cost, 2);
assert.equal(view.status.sessionTokens, 8);
const localView = projectConversation({ local, activeTabId: "local", backgroundRuntimes: background, connectingLabel: "connecting" });
assert.equal(localView.runtime, local);
assert.equal(localView.status.backgroundRuntimes, background);
assert.equal(localView.composer.attachmentInputEnabled, true);
assert.equal(localView.composer.localDurableGuidance, true);
assert.equal(localView.context.tabId, "local");
const readableBeforeRuntime = projectNavigationSurfaceTarget({
  activeTabId: "local",
  sessionKey: "local:session",
  local: {
    ...local,
    meta: { ...local.meta!, ready: false, runtime: { phase: "starting", epoch: "runtime-1" } },
    backendActivationPending: true,
    hydrating: false,
    hydrateError: undefined,
  },
});
assert.equal(readableBeforeRuntime.ready, true, "local navigation paint is released by readable history rather than runtime readiness");
assert.equal(readableBeforeRuntime.backendActivationPending, false, "local runtime activation does not block transcript paint");
const localHistoryPending = projectNavigationSurfaceTarget({
  activeTabId: "local",
  sessionKey: "local:session",
  local: { ...local, backendActivationPending: true, hydrating: true, hydrateError: undefined },
});
assert.equal(localHistoryPending.ready, false, "local cache miss keeps only the target history skeleton pending");
for (const chatVisible of [false, true]) for (const localToolsEnabled of [false, true]) for (const dockMode of ["files", "changed", "remote", "context"]) {
  const layout = projectConversationLayout({ chatVisible, localToolsEnabled, dockMode, dockRenderable: true,
    dockGridOpen: true, dockOverlay: true, dockOpen: true, dockMaximized: true, terminalOpen: true });
  const permitted = chatVisible && (localToolsEnabled || !["files", "changed"].includes(dockMode));
  assert.equal(layout.dockVisible, permitted);
  assert.equal(layout.dockGridOpen, permitted);
  assert.equal(layout.dockOverlay, permitted);
  assert.equal(layout.terminalOpen, chatVisible && localToolsEnabled);
  assert.equal(layout.dockMaximized, chatVisible, "automation masks stored maximization without modifying the preference");
  if (!localToolsEnabled && (dockMode === "files" || dockMode === "changed")) {
    const noop = () => {};
    const markup = renderToStaticMarkup(createElement(WorkspaceDockRegion, {
      visible: layout.dockVisible, overlay: layout.dockOverlay, mode: dockMode,
      showContext: true, t: ((key: string) => key) as Translator,
      onPickEntry: noop, remote: { onClose: noop }, context: view.context,
      workspaceKey: "fixture", workspace: { open: layout.dockVisible, maximized: false, onClose: noop, onToggleMaximized: noop },
    }));
    assert.equal(markup, "", "actual dock region never mounts local Files/Changes for a remote source");
  }
}
console.log("conversation projection: shared source identity, remote telemetry isolation and local-tool layout policy passed");
