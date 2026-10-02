// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/file-navigation-lifecycle.test.tsx
//
// The historical defect was a request object rebuilt during render and merged
// into the panel's props: every render produced a new object, the panel wrote
// state from it, and the pair looped until React reported #301. This drives the
// real dock chain and asserts the replacement behaves: a navigation happens
// once, re-renders neither navigate nor read, and a StrictMode mount does not
// double a read.

import assert from "node:assert/strict";
import React, { StrictMode, act } from "react";
import { renderFilesWorkspace, waitFor, flushPromises } from "./workspace-panel-test-harness";
import { WorkspaceDockRegion, type WorkspaceDockRegionProps } from "../app-shell/WorkspaceDockRegion";
import { PresentedFiles } from "../components/PresentedFiles";
import { LocaleProvider } from "../lib/i18n";
import { performResourceAction } from "../lib/fileNavigationCommands";
import { fileNavigationOwner } from "../lib/fileNavigationCommands";
import { fileNavigationKey } from "../lib/fileNavigationOwner";
import { useActivityBarStore } from "../store/activityBar";

const reads: string[] = [];
const preview = (path: string) => {
  reads.push(path);
  return { path, body: `CONTENT ${path}`, size: 12, truncated: false, binary: false };
};
const { dom, root, dockTabId } = await renderFilesWorkspace({
  ReadFileForTab: async (_tab, path) => preview(path),
  ReadPresentedFileForTab: async (_tab, _tool, path) => preview(path),
  ReadPresentedFileSourceForTab: async (_tab, _tool, path) => preview(path),
});
const props: WorkspaceDockRegionProps = {
  visible: true, overlay: false, mode: "files", showContext: false,
  t: key => key, onPickEntry: () => {}, remote: { onClose: () => {} }, context: {} as WorkspaceDockRegionProps["context"],
  workspace: { open: true, tabId: "tab-a", cwd: "/repo", maximized: false, onClose: () => {}, onToggleMaximized: () => {} },
  workspaceKey: "lifecycle", workspaceRoot: "/repo", fileNavigation: fileNavigationOwner(),
};
let paints = 0;
function Lifecycle({ generation }: { generation: number }) {
  paints += 1;
  return <>
    <PresentedFiles tabId="tab-a" hostId="local" files={[{ path: "app.ts", toolCallId: "call", description: "d" }]} />
    <span data-generation={generation} />
    <WorkspaceDockRegion {...props} />
  </>;
}
const record = () => fileNavigationOwner().getSnapshot(fileNavigationKey({ sessionTabId: "tab-a", dockTabId }));
const paint = (generation: number) => act(async () => {
  root.render(<StrictMode><LocaleProvider><Lifecycle generation={generation} /></LocaleProvider></StrictMode>);
  await flushPromises();
});

// StrictMode replays mount effects; the dock must still open the file once.
await paint(0);
await act(async () => {
  document.querySelector<HTMLButtonElement>(".presented-file__main")
    ?.dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  await flushPromises();
});
await waitFor("presented preview", () => document.body.textContent?.includes("CONTENT app.ts") === true);
const afterOpen = record()!;
assert.equal(afterOpen.selected?.resource.path, "app.ts");
assert.equal(reads.length, 1, `one command reads the file once, got ${JSON.stringify(reads)}`);
assert.equal(afterOpen.navigation?.revision, 1, "one command is one navigation revision");

// Repeated parent renders must not navigate, read again, or loop. StrictMode
// renders each commit twice, so the bound is two paints per driven render; a
// render loop would overshoot it by orders of magnitude.
const readsAfterOpen = reads.length;
const paintsAfterOpen = paints;
for (let generation = 1; generation <= 12; generation += 1) await paint(generation);
assert.equal(reads.length, readsAfterOpen, `parent re-renders must not read again, got ${JSON.stringify(reads)}`);
assert.equal(record()!.navigation?.revision, 1, "parent re-renders never advance a navigation revision");
assert(paints - paintsAfterOpen <= 24, `twelve driven renders paint at most twice each, got ${paints - paintsAfterOpen}`);

// A second click on the same row re-delivers the navigation but not the read.
await act(async () => {
  document.querySelector<HTMLButtonElement>(".presented-file__main")
    ?.dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  await flushPromises();
});
await waitFor("repeat preview", () => record()!.navigation!.revision === 2);
assert.equal(reads.length, readsAfterOpen, "reopening the same file in the same mode keeps the valid read");

// A remount restores the committed selection without replaying the command.
await act(async () => { root.render(<StrictMode><LocaleProvider><span /></LocaleProvider></StrictMode>); await flushPromises(); });
const readsAfterUnmount = reads.length;
await paint(13);
await waitFor("restored preview", () => document.body.textContent?.includes("CONTENT app.ts") === true);
assert.equal(record()!.navigation?.revision, 2, "a remount restores the record instead of issuing a command");
assert.equal(reads.length, readsAfterUnmount + 1, "a remount reads the restored selection exactly once");
assert.equal(record()!.dockTabId, useActivityBarStore.getState().activeTabId);

await act(async () => root.unmount());
dom.window.close();
console.log("PASS dock navigation is command-driven: no render-time requests, no re-render reads, no StrictMode replay");
