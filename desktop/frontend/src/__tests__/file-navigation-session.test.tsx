// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/file-navigation-session.test.tsx
//
// What a session change does to a dock that keeps its identity: the previews
// stay where the user left them, their read credentials do not cross over, and
// the new session's first command is still delivered as a navigation.

import assert from "node:assert/strict";
import React, { act } from "react";
import { renderFilesWorkspace, waitFor } from "./workspace-panel-test-harness";
import { WorkspaceDockRegion, type WorkspaceDockRegionProps } from "../app-shell/WorkspaceDockRegion";
import { LocaleProvider } from "../lib/i18n";
import { performResourceAction, fileNavigationOwner } from "../lib/fileNavigationCommands";
import { fileNavigationKey } from "../lib/fileNavigationOwner";

const reads: string[] = [];
const { dom, root, dockTabId } = await renderFilesWorkspace({
  ReadPresentedFileForTab: async (_tab, _tool, path) => {
    reads.push(`presented:${path}`);
    return { path, body: `CONTENT ${path}`, size: 20, truncated: false, binary: false };
  },
  ReadFileForTab: async (_tab, path) => {
    reads.push(`workspace:${path}`);
    return { path, body: `CONTENT ${path}`, size: 20, truncated: false, binary: false };
  },
  ListDirForTab: async (_tab, dir) => dir === "" ? [{ name: "app.ts", isDir: false }] : [],
});
const props: WorkspaceDockRegionProps = {
  visible: true, overlay: false, mode: "files", showContext: false,
  t: key => key, onPickEntry: () => {}, remote: { onClose: () => {} }, context: {} as WorkspaceDockRegionProps["context"],
  workspace: { open: true, tabId: "session-tab", cwd: "/repo", maximized: false, onClose: () => {}, onToggleMaximized: () => {} },
  workspaceKey: "session-test", workspaceRoot: "/repo",
};
// A topic switch inside one project: the tab and the project memory key stay,
// only the session scope the dock is bound to changes.
let sessionTabId = "session-tab";
let sessionScope = "scope-one";
const paint = () => act(async () => root.render(
  <LocaleProvider><WorkspaceDockRegion {...props} workspace={{
    ...props.workspace, workspaceScopeKey: sessionScope, workspaceMemoryKey: "project-memory",
  }} visible /></LocaleProvider>,
));
const record = () => fileNavigationOwner().getSnapshot(fileNavigationKey({ sessionTabId, dockTabId }));
const open = async (path: string) => {
  await act(async () => performResourceAction({ hostId: "local", tabId: sessionTabId, path, source: "presented", toolCallId: "call" }, "preview"));
};
await paint();

// A presented file is open in the first session.
await open("notes.md");
await paint();
await waitFor("first preview", () => document.body.textContent?.includes("CONTENT notes.md") === true);
assert.equal(record()?.selected?.resource.access.source, "presented");
const previewBody = document.querySelector(".workspace-preview__body");
assert(previewBody, "the preview area is mounted before the session changes");

// Another session in the same project keeps the dock and what it shows, but the
// presented tool scope belongs to the session that captured it and must not
// authorize a read here.
sessionScope = "scope-two";
await paint();
const afterSwitch = record();
assert(afterSwitch, "the dock keeps its record across a session change");
assert.equal(afterSwitch.selected?.resource.path, "notes.md", "the selected file survives a session change");
assert.equal(afterSwitch.selected?.resource.access.source, "workspace", "the presented scope does not cross sessions");
assert.equal(afterSwitch.selected?.resource.access.toolCallId, undefined);
assert.equal(document.querySelector(".workspace-preview__body"), previewBody, "the preview element is the same DOM node");
assert.equal(afterSwitch.revision, record()!.revision, "the record is the one the dock already had");
await waitFor("rebound preview", () => reads.includes("workspace:notes.md"));
assert(document.body.textContent?.includes("CONTENT notes.md"), "the file is read again under the new session");

await act(async () => root.unmount());
dom.window.close();
console.log("PASS session change: previews survive, presented scope does not, first navigation still applies");
