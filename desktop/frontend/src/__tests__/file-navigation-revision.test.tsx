// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/file-navigation-revision.test.tsx
//
// A dock record's revision counter belongs to that record: when the dock reads
// a different record, its counter starts over, and the panel must not mistake
// the new record's first command for a revision it already applied.

import assert from "node:assert/strict";
import React, { act } from "react";
import { renderFilesWorkspace } from "./workspace-panel-test-harness";
import { WorkspaceDockRegion, type WorkspaceDockRegionProps } from "../app-shell/WorkspaceDockRegion";
import { LocaleProvider } from "../lib/i18n";
import { performResourceAction, fileNavigationOwner } from "../lib/fileNavigationCommands";
import { fileNavigationKey } from "../lib/fileNavigationOwner";
import { useActivityBarStore } from "../store/activityBar";

const { dom, root, dockTabId } = await renderFilesWorkspace({
  ReadPresentedFileForTab: async (_tab, _tool, path) => ({ path, body: `CONTENT ${path}`, size: 20, truncated: false, binary: false }),
  ListDirForTab: async () => [],
});
const props: WorkspaceDockRegionProps = {
  visible: true, overlay: false, mode: "files", showContext: false,
  t: key => key, onPickEntry: () => {}, remote: { onClose: () => {} }, context: {} as WorkspaceDockRegionProps["context"],
  workspace: { open: true, tabId: "tab-one", cwd: "/repo", maximized: false, onClose: () => {}, onToggleMaximized: () => {} },
  workspaceKey: "revision-test", workspaceRoot: "/repo",
};
const sessionTabId = "tab-one";
const paint = () => act(async () => root.render(
  <LocaleProvider><WorkspaceDockRegion {...props} workspace={{ ...props.workspace, tabId: sessionTabId }} visible /></LocaleProvider>,
));
const record = () => fileNavigationOwner().getSnapshot(fileNavigationKey({ sessionTabId, dockTabId }));
const open = async (path: string) => {
  await act(async () => performResourceAction({ hostId: "local", tabId: sessionTabId, path, source: "presented", toolCallId: "call" }, "preview"));
};
const onChangesView = () => document.querySelector(".workspace-panel--changed-overview") !== null;

await paint();
await open("first.md");
await paint();
assert.equal(record()?.navigation?.revision, 1, "the dock's first navigation is revision one");

// The user moves to the Changes view, then the dock tab is closed and reopened:
// the dock reads a new record, and the revision is one sequence for the whole
// instance, so that record cannot hand out a revision the panel already applied.
const changesView = [...document.querySelectorAll<HTMLButtonElement>("button")]
  .find(button => /^changes$/i.test((button.textContent ?? "").trim()));
assert(changesView, "the dock exposes its Changes view");
await act(async () => { changesView!.dispatchEvent(new window.MouseEvent("click", { bubbles: true })); });
assert(onChangesView(), "the dock is on the Changes view");
const closedGeneration = record()!.generation;
await act(async () => {
  useActivityBarStore.getState().closeTab(dockTabId);
  fileNavigationOwner().retain([]);
  await Promise.resolve();
});
assert.equal(record(), null, "a closed dock tab keeps no record");
await act(async () => useActivityBarStore.getState().reopenTab(dockTabId));
await paint();
assert.equal(record()?.generation, closedGeneration + 1, "reopening starts a new dock lifetime");
await open("second.md");
await paint();
assert.equal(record()?.navigation?.revision, 2, "the new record's navigation continues the instance's sequence");
assert.equal(record()?.selected?.resource.path, "second.md");
assert(!onChangesView(), "that command is applied: the dock returns to its file preview");
assert(document.body.textContent?.includes("CONTENT second.md") === true, "and shows the file it opened");

await act(async () => root.unmount());
dom.window.close();
console.log("PASS a dock reading another record still applies that record's first navigation");
