// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/file-navigation-dock.test.tsx
//
// The preview-tab experience behind the navigation record: reopening activates
// the existing tab, the cap and its ordering are kept, source mode reuses the
// tab, a reveal expands the tree, and closing then reopening the dock tab is a
// new lifecycle generation rather than a restored one.

import assert from "node:assert/strict";
import React, { act } from "react";
import { renderFilesWorkspace, waitFor } from "./workspace-panel-test-harness";
import { WorkspaceDockRegion, type WorkspaceDockRegionProps } from "../app-shell/WorkspaceDockRegion";
import { LocaleProvider } from "../lib/i18n";
import { performResourceAction } from "../lib/fileNavigationCommands";
import { fileNavigationOwner } from "../lib/fileNavigationCommands";
import { FILE_PREVIEW_LIMIT, fileNavigationKey } from "../lib/fileNavigationOwner";
import { useActivityBarStore } from "../store/activityBar";

const reads: string[] = [];
const { dom, root, dockTabId } = await renderFilesWorkspace({
  ReadPresentedFileForTab: async (_tab, _tool, path) => {
    reads.push(`preview:${path}`);
    return { path, body: `preview content ${path}`, size: 24, truncated: false, binary: false };
  },
  ReadPresentedFileSourceForTab: async (_tab, _tool, path) => {
    reads.push(`source:${path}`);
    return { path, body: `source content ${path}`, size: 24, truncated: false, binary: false };
  },
  ListDirForTab: async (_tab, dir) => dir === "" ? [{ name: "app.ts", isDir: false }] : [],
});
const props: WorkspaceDockRegionProps = {
  visible: true, overlay: false, mode: "files", showContext: false,
  t: key => key, onPickEntry: () => {}, remote: { onClose: () => {} }, context: {} as WorkspaceDockRegionProps["context"],
  workspace: { open: true, tabId: "navigation-session", cwd: "/repo", maximized: false, onClose: () => {}, onToggleMaximized: () => {} },
  workspaceKey: "tabs-test", workspaceRoot: "/repo",
};
let sessionTabId = "navigation-session";
const paint = (visible: boolean) => act(async () => root.render(
  <LocaleProvider><WorkspaceDockRegion {...props} workspace={{ ...props.workspace, tabId: sessionTabId }} visible={visible} /></LocaleProvider>,
));
const record = () => fileNavigationOwner().getSnapshot(fileNavigationKey({ sessionTabId, dockTabId }));
const paths = () => record()?.entries.map((entry) => entry.resource.path) ?? [];
const open = async (path: string, action: "preview" | "source" | "reveal-tree" = "preview") => {
  await act(async () => performResourceAction({ hostId: "local", tabId: sessionTabId, path, source: "presented", toolCallId: "call" }, action));
  await paint(true);
};
await paint(true);

// ── Consecutive A / B / A: one tab each, most recent last ──
await open("a.md");
await open("b.md");
await open("a.md");
assert.deepEqual(paths(), ["b.md", "a.md"], "reopening activates the existing tab instead of adding one");
await waitFor("reactivated preview", () => document.body.textContent?.includes("preview content a.md") === true);

// ── Source mode reuses the tab and reads the other representation ──
const sourceToggle = [...document.querySelectorAll<HTMLButtonElement>(".workspace-preview__window-actions button")]
  .find(button => /source/i.test(button.getAttribute("aria-label") ?? ""));
assert(sourceToggle, "a presented preview exposes its source toggle");
await act(async () => { sourceToggle!.dispatchEvent(new window.MouseEvent("click", { bubbles: true })); });
await waitFor("source preview", () => document.body.textContent?.includes("source content a.md") === true);
assert.deepEqual(paths(), ["b.md", "a.md"], "switching the mode reuses the file tab");
assert(reads.includes("source:a.md"), "the source read used the source entry point");

// ── reveal-tree expands the rail and locates the file ──
const hideTree = [...document.querySelectorAll<HTMLButtonElement>("button")]
  .find(button => /hide file tree/i.test(button.getAttribute("aria-label") ?? ""));
if (hideTree) await act(async () => { hideTree.dispatchEvent(new window.MouseEvent("click", { bubbles: true })); });
await open("a.md", "reveal-tree");
assert(document.querySelector(".workspace-tree") !== null, "reveal-tree shows the file tree");
assert.equal(document.querySelector('[data-workspace-path="app.ts"]') !== null, true, "reveal-tree lists the workspace file");

// ── The preview tab list keeps its cap and its order ──
for (const path of ["c.txt", "d.txt", "e.txt", "f.txt", "g.txt"]) await open(path);
assert.equal(paths().length, FILE_PREVIEW_LIMIT, "the dock keeps the preview tab limit");
assert.deepEqual(paths(), ["c.txt", "d.txt", "e.txt", "f.txt", "g.txt"], "the cap keeps the most recently used tabs in order");
assert.equal(document.querySelectorAll(".workspace-document-tab").length, FILE_PREVIEW_LIMIT, "the tab strip shows the same list");
const readsBeforeRepeat = reads.length;
await open("c.txt");
assert.deepEqual(paths(), ["d.txt", "e.txt", "f.txt", "g.txt", "c.txt"], "reopening moves the tab to the most recent position");
assert(reads.length > readsBeforeRepeat, "activating a tab reads it again for this dock");

// ── Closing the dock tab and reopening it is a new lifecycle generation ──
const closedGeneration = record()!.generation;
await act(async () => {
  useActivityBarStore.getState().closeTab(dockTabId);
  // The runtime keeps the open dock tabs of the active session retained; a
  // standalone region mount has no runtime, so the test reconciles for it.
  fileNavigationOwner().retain([]);
  await Promise.resolve();
});
await paint(false);
assert.equal(record(), null, "a closed dock tab keeps no record");
// Reopening from the recently-closed list reuses the same tab id.
await act(async () => useActivityBarStore.getState().reopenTab(dockTabId));
assert.equal(useActivityBarStore.getState().activeTabId, dockTabId, "the reopened tab reuses its id");
await paint(true);
await act(async () => performResourceAction({ hostId: "local", tabId: "navigation-session", path: "h.txt", source: "presented", toolCallId: "call" }, "preview"));
await paint(true);
const reopened = record()!;
assert(reopened.generation > closedGeneration, "a reopened dock tab starts a new lifecycle generation");
// The remembered paths come back, but only as workspace files: the presented
// tool scope an earlier session opened them with is never restored.
const rememberedEntries = reopened.entries.filter(entry => entry.resource.path !== "h.txt");
assert(rememberedEntries.length > 0, "the reopened dock restores the remembered paths");
assert(rememberedEntries.every(entry => entry.resource.access.source === "workspace" && entry.resource.access.toolCallId === undefined),
  "restored entries carry workspace access only");
assert.equal(reopened.selected?.resource.path, "h.txt");
assert.equal(reopened.selected?.resource.access.source, "presented", "the new command's own access context is used");

// ── Collapsing the dock keeps the record; expanding restores it ──
await paint(false);
await paint(true);
const expanded = record()!;
assert.equal(expanded.generation, reopened.generation, "a collapse is not a new lifecycle");
assert.deepEqual(expanded.entries.map(entry => entry.resource.path), reopened.entries.map(entry => entry.resource.path),
  "expanding restores the committed previews");
assert.equal(expanded.selected?.resource.path, "h.txt", "expanding restores the committed selection");
assert.equal(expanded.navigation?.revision, reopened.navigation?.revision, "restoring never replays the command that opened it");

await act(async () => root.unmount());
dom.window.close();
console.log("PASS dock preview tabs: reuse, cap, source mode, reveal, lifecycle generations, collapse restore and first navigation per session");
