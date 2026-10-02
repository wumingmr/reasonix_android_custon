import assert from "node:assert/strict";
import React, { act } from "react";
import { renderFilesWorkspace, waitFor } from "./workspace-panel-test-harness";
import { WorkspaceDockRegion, type WorkspaceDockRegionProps } from "../app-shell/WorkspaceDockRegion";
import { LocaleProvider } from "../lib/i18n";
import { performResourceAction } from "../lib/presentedFileNavigation";
import { fileNavigationOwner } from "../lib/fileNavigationCommands";
import { fileNavigationKey } from "../lib/fileNavigationOwner";
import { useActivityBarStore } from "../store/activityBar";
import { useRemoteStore } from "../store/remote";

const reads: string[] = [];
const preview = (path: string, mode: string) => {
  reads.push(`${mode}:${path}`);
  return { path, body: `${mode} content ${path}`, size: 20, truncated: false, binary: false };
};
const { dom, root } = await renderFilesWorkspace({
  ReadPresentedFileForTab: async (_tab, _tool, path) => preview(path, "preview"),
  ReadPresentedFileSourceForTab: async (_tab, _tool, path) => preview(path, "source"),
  ListRemoteDir: async () => [],
  ReadRemoteFile: async (_host, path) => ({ ...preview(path, "remote"), mtimeUnix: 1 }),
  ResolveRemoteWorkspacePathForTab: async (_tab, _host, _tool, path) => {
    if (path === "denied.txt") throw new Error("permission denied");
    return path;
  },
  ResolveRemotePresentedPathForTab: async (_tab, _host, _tool, path) => path,
});
const props: WorkspaceDockRegionProps = {
  visible: false, overlay: false, mode: "files", showContext: false,
  t: key => key, onPickEntry: () => {}, remote: { onClose: () => {} }, context: {} as WorkspaceDockRegionProps["context"],
  workspace: { open: true, tabId: "navigation-session", cwd: "/repo", maximized: false, onClose: () => {}, onToggleMaximized: () => {} },
  workspaceKey: "navigation-test", workspaceRoot: "/repo",
};
const paint = (visible: boolean) => act(async () => root.render(<LocaleProvider><WorkspaceDockRegion {...props} visible={visible} /></LocaleProvider>));
// The committed record of whichever dock the command targeted.
const committed = (dockTabId: string | null | undefined) => dockTabId
  ? fileNavigationOwner().getSnapshot(fileNavigationKey({ sessionTabId: "navigation-session", dockTabId }))
  : null;
await act(async () => { useActivityBarStore.setState({ workspaceRoot: "/repo", tabs: [], activeTabId: null }); });
await paint(false);
for (const hostId of ["local", "remote-test"]) {
  await act(async () => useRemoteStore.setState({ statuses: { "remote-test": { state: "connected" } } }));
  for (const action of ["preview", "source", "reveal-tree"] as const) {
    const path = `${hostId}-${action}.txt`;
    await act(async () => performResourceAction({ hostId, tabId: "navigation-session", path, source: "presented", toolCallId: "call" }, action));
    await paint(true);
    await waitFor(path, () => document.body.textContent?.includes(`content ${path}`) === true);
    const record = committed(useActivityBarStore.getState().activeTabId);
    assert.equal(record?.selected?.resource.path, path, "the targeted dock committed the opened resource");
    assert.equal(record?.selected?.resource.access.source, "presented");
    assert.equal(record?.selected?.resource.access.toolCallId, "call");
    await paint(true);
    assert(document.body.textContent?.includes(`content ${path}`));
    console.log("PASS real dock", hostId, action);
  }
  if (hostId === "local") {
    const original = useActivityBarStore.getState().activeTabId!;
    await act(async () => useActivityBarStore.getState().addTab("file", "Other files"));
    await act(async () => useActivityBarStore.getState().activateTab(original));
    await waitFor("restored presented resource", () => document.body.textContent?.includes("content local-reveal-tree.txt") === true);
    console.log("PASS presented resource identity survives view remount without replay");
    await act(async () => performResourceAction({ hostId, tabId: "navigation-session", path: "src/alias.ts", source: "workspace" }, "preview"));
    await act(async () => performResourceAction({ hostId, tabId: "navigation-session", path: "/repo/src/alias.ts", source: "workspace" }, "preview"));
    const aliasRecord = committed(useActivityBarStore.getState().activeTabId)!;
    assert.equal(aliasRecord.entries.filter((entry) => entry.resource.identityPath === "/repo/src/alias.ts").length, 1,
      "canonical identity deduplicates relative and absolute spellings through the real command path");
  }
}
// A remote workspace path the host refuses reports its failure to the row that
// asked, and commits nothing the dock could render.
await paint(false);
const denied = await performResourceAction({ hostId: "remote-test", tabId: "navigation-session", path: "denied.txt", source: "workspace", toolCallId: "call" }, "preview");
assert.equal(denied.status, "failed");
assert.match((denied as { error: Error }).error.message, /permission denied/);
assert(!committed(useActivityBarStore.getState().activeTabId)?.entries.some(entry => entry.resource.path === "denied.txt"),
  "a refused path never reaches the dock's record");

await act(async () => root.unmount());
dom.window.close();
assert(reads.length >= 6);
