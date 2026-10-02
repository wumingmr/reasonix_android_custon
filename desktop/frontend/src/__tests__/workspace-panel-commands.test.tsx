import { installDesktopHostStub } from "./desktopHostStub";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useWorkspacePanelCommands } from "../app-runtime/useWorkspacePanelCommands";
import { useSessionNavigationCommands, type SessionNavigationCommandsInput } from "../app-runtime/useSessionNavigationCommands";
import { loadWorkspacePanelOpen, saveWorkspacePanelOpen, useLayoutStore } from "../store/layout";
import { useActivityBarStore } from "../store/activityBar";
import { useRemoteStore } from "../store/remote";
import type { RemoteHostView } from "../lib/types";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage,
  IS_REACT_ACT_ENVIRONMENT: true });
const root = createRoot(document.getElementById("root")!);
let commands!: ReturnType<typeof useWorkspacePanelCommands>;
let closes = 0; let widthClears = 0;
const closeOverlays = () => { closes++; };
const clearLiveWidth = () => { widthClears++; };
let restoredWidth = 0;
const globalRoot = "/fixture/global-workspace";
let navigation!: ReturnType<typeof useSessionNavigationCommands>;
let navigationRequest: unknown;
let creationTarget:unknown;
let intent=0;
installDesktopHostStub(makeSessionUIMock(async (scope,workspaceRoot)=>{creationTarget={scope,workspaceRoot};}));
const setTreeWidth = (width: number) => { restoredWidth = width; };
function Probe({ workspace, visible, sessionId }: { workspace: string; visible: boolean; sessionId: string }) {
  commands = useWorkspacePanelCommands({ sessionId, workspaceRoot: workspace, visible, closeOverlays, clearLiveWidth,
    availableWidth: 800, clampTreeWidth: (width) => width, setTreeWidth, gridOpen: visible, t: (key: string) => key } as never);
  navigation = useSessionNavigationCommands({
    activeTab: { id: "fixture", scope: workspace === globalRoot ? "global" : "project", workspaceRoot: workspace },
    closeTransientOverlays: closeOverlays, clearImDetail: () => {}, prepareBlankWorkspace: commands.prepareBlankWorkspace,
    enterConversation: () => {},
    beginNavigationSurface: () => {}, settleNavigationSurface: () => {},
    noteNavigationIntent:()=>++intent, isNavigationIntentCurrent:(seq:number)=>seq===intent, markProjectChanged:()=>{}, showToast:(error:string)=>{throw Error(error);},
    navigation: { enqueueNavigationWithIntent: async request => { navigationRequest = request; } },
  } as SessionNavigationCommandsInput);
  return null;
}
const paint = (workspace: string, visible = false, sessionId = `session:${workspace}`) =>
  act(async () => root.render(<Probe workspace={workspace} visible={visible} sessionId={sessionId} />));
try {
  saveWorkspacePanelOpen(false, "A"); saveWorkspacePanelOpen(true, "B");
  await paint("A");
  const first = commands;
  assert.equal(useLayoutStore.getState().workspacePanelOpen, false);
  await act(async () => commands.openRightDockMode("changed"));
  assert.equal(loadWorkspacePanelOpen("A"), true);
  await paint("A", true);
  await act(async () => { commands.toggleWorkspaceMaximized(); commands.handleWorkspacePreviewModeChange(true); });
  assert.equal(useLayoutStore.getState().workspacePanelMaximized, true);
  await act(async () => commands.openRightDockMode("context"));
  assert.equal(useLayoutStore.getState().workspacePanelMaximized, false);
  assert.equal(useLayoutStore.getState().workspacePreviewActive, false);
  await act(async () => commands.toggleWorkspacePanel());
  assert.equal(loadWorkspacePanelOpen("A"), false);
  assert.equal(widthClears, 1);
  await paint("B");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "different project restores its own preference");
  await paint("B", true);
  assert.deepEqual(useActivityBarStore.getState().tabs.map(tab => tab.type), ["context"],
    "an expanded empty dock opens Overview when a session becomes active");
  const overviewTabId = useActivityBarStore.getState().activeTabId!;
  await act(async () => useActivityBarStore.getState().closeTab(overviewTabId));
  await paint("B", true);
  assert.deepEqual(useActivityBarStore.getState().tabs, [],
    "closing the default tab does not reopen it during the same session");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, false,
    "closing the final dock tab collapses the entire workspace panel");
  assert.equal(loadWorkspacePanelOpen("B"), false, "automatic collapse persists for the current project");
  await paint("B", true, "session:B:next");
  assert.deepEqual(useActivityBarStore.getState().tabs, [], "a new session preserves the collapsed dock");
  await act(async () => commands.toggleWorkspacePanel());
  await paint("B", true, "session:B:next");
  assert.deepEqual(useActivityBarStore.getState().tabs.map(tab => tab.type), ["context"],
    "explicitly reopening the dock in a new session seeds Overview");
  await act(async () => commands.openRightDockMode("files"));
  await act(async () => useActivityBarStore.getState().closeTab(useActivityBarStore.getState().activeTabId!));
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "closing one of multiple tabs keeps the dock open");
  await act(async () => {
    commands.openRightDockMode("changed");
    commands.toggleWorkspaceMaximized();
  });
  const beforeCloseAll = widthClears;
  await act(async () => {
    useActivityBarStore.getState().tabs.forEach(tab => useActivityBarStore.getState().closeTab(tab.id));
    assert.equal(useLayoutStore.getState().workspacePanelOpen, false, "close-all collapses synchronously");
  });
  assert.equal(useLayoutStore.getState().workspacePanelMaximized, false, "closing all tabs resets maximized mode");
  assert.equal(widthClears, beforeCloseAll + 1, "automatic collapse clears the live resize width once");
  await act(async () => commands.openDockEntry("files"));
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "opening an entry expands the dock again");
  assert.deepEqual(useActivityBarStore.getState().tabs.map(tab => tab.type), ["file"]);
  await act(async () => {
    useActivityBarStore.getState().closeTab(useActivityBarStore.getState().activeTabId!);
    commands.openRightDockMode("changed");
  });
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "a later open in the same batch wins over the close");
  assert.equal(loadWorkspacePanelOpen("B"), true);
  saveWorkspacePanelOpen(true, "empty-project");
  await paint("empty-project");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true,
    "restoring an empty project is not mistaken for closing the final tab");
  assert.equal(loadWorkspacePanelOpen("B"), true, "project restoration does not overwrite the previous preference");
  await paint("A");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, false);
  assert.equal(commands.closeWorkspacePanel, first.closeWorkspacePanel);
  assert.equal(commands.openRightDockMode, first.openRightDockMode);
  await act(async () => commands.openRightDockMode("files"));
  const hosts = [{ id: "offline" }, { id: "online" }] as RemoteHostView[];
  await act(async () => {
    useRemoteStore.getState().setHosts(hosts);
    useRemoteStore.getState().applyStatus({ hostId: "online", state: "connected" });
    commands.openRemoteDock();
  });
  assert.equal(useRemoteStore.getState().explorerHostId, "online");
  assert.equal(useRemoteStore.getState().explorerOpen, false, "request is consumed by the same dock owner");
  assert.equal(useLayoutStore.getState().rightDockMode, "remote");
  await act(async () => { commands.restoreWorkspaceDockWidths(640, 0); });
  assert.equal(restoredWidth, 640, "dock width restore clamps through the owner and writes the layout store port");
  await act(async () => useRemoteStore.getState().setHosts([]));
  assert.equal(useLayoutStore.getState().rightDockMode, "files");
  await paint("A");
  await act(async () => commands.openRightDockMode("changed"));
  saveWorkspacePanelOpen(true, "B");
  await act(async () => {
    commands.toggleWorkspaceMaximized();
    commands.prepareBlankWorkspace("B");
  });
  assert.equal(useLayoutStore.getState().workspacePanelOpen, false, "new-session intent collapses the dock immediately");
  assert.equal(useLayoutStore.getState().workspacePanelMaximized, false);
  assert.equal(loadWorkspacePanelOpen("A"), true, "another project's preference is untouched");
  await paint("B");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, false, "destination restoration cannot reopen the blank-session dock");
  await act(async () => commands.openRightDockMode("files"));
  await paint("B");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "manual open stays open on subsequent renders");
  await paint("A");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "ordinary project navigation retains restoration behavior");
  saveWorkspacePanelOpen(true, "");
  saveWorkspacePanelOpen(true, globalRoot);
  await act(async () => navigation.openBlankSession("global", globalRoot));
  assert.deepEqual(creationTarget, { scope: "global", workspaceRoot: "" }, "global formal creation uses the empty root contract");
  assert.ok(navigationRequest, "local new-session navigates to its formal identity");
  assert.equal(loadWorkspacePanelOpen(""), true, "global creation does not overwrite the legacy fallback for other projects");
  await paint(globalRoot);
  assert.equal(useLayoutStore.getState().workspacePanelOpen, false, "global destination restoration cannot reopen the new-session dock");
  await act(async () => commands.openRightDockMode("files"));
  await paint("A");
  await paint(globalRoot);
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "manual global preference still restores on ordinary navigation");
  await act(async () => navigation.handleNewTab());
  assert.equal(loadWorkspacePanelOpen(globalRoot), false, "new-session toolbar uses the active global directory");
  assert.deepEqual(creationTarget, { scope: "global", workspaceRoot: "" });
  await paint("A");
  assert.equal(useLayoutStore.getState().workspacePanelOpen, true, "global creation preserves the source project's preference");
  await act(async () => root.unmount());
  const before = { closes, widthClears, layout: useLayoutStore.getState() };
  first.openRightDockMode("changed"); first.toggleWorkspaceMaximized(); first.closeWorkspacePanel();
  assert.deepEqual({ closes, widthClears, layout: useLayoutStore.getState() }, before, "disposed entries cannot change layout or project preferences");
  console.log("workspace commands: scoped restoration, preview/maximize, remote requests and synchronous disposal passed");
} finally { dom.window.close(); }
