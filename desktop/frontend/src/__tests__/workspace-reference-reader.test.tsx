// Run: tsx src/__tests__/workspace-reference-reader.test.tsx
//
// Selecting an answer reference must re-read the file through the reference
// reader even when that path is already open, because only the reference read
// re-resolves the path on the host. A ref-backed reader flag would leave the
// stale workspace read on screen.
import { act } from "react";
import { flushPromises, renderFilesWorkspace, waitFor } from "./workspace-panel-test-harness";
import { fileNavigationOwner } from "../lib/fileNavigationCommands";

const reads: string[] = [];
const preview = (path: string, body: string) => {
  reads.push(body);
  return { path, body, size: body.length, truncated: false, binary: false };
};

const { dom, root, dockTabId } = await renderFilesWorkspace({
  ReadFileForTab: async (_tabId, path) => preview(path, "WORKSPACE READ"),
  ReadReferenceFileForTab: async (_tabId, path) => preview(path, "REFERENCE READ"),
  ReadReferenceFileSourceForTab: async (_tabId, path) => preview(path, "REFERENCE SOURCE"),
  ResolveReferencePathForTab: async (_tabId, path) => `/repo/${path}`,
});

const scope = { sessionTabId: "tab-a", dockTabId };
await act(async () => {
  await fileNavigationOwner().openIn(scope, {
    ref: { source: "workspace", hostId: "local", tabId: "tab-a", path: "docs/note.md" },
    params: { action: "preview", view: "files" },
  });
  await flushPromises();
});

await waitFor("workspace read", () => document.body.textContent?.includes("WORKSPACE READ") === true);

// Same path, now owned by a verified answer reference.
await act(async () => {
  await fileNavigationOwner().openIn(scope, {
    ref: { source: "reference", hostId: "local", tabId: "tab-a", path: "docs/note.md" },
    params: { action: "preview", view: "files" },
  });
  await flushPromises();
});
await waitFor("reference read", () => document.body.textContent?.includes("REFERENCE READ") === true);
if (reads.filter(entry => entry === "REFERENCE READ").length !== 1) {
  throw new Error(`the reference reader was not used exactly once: ${JSON.stringify(reads)}`);
}

// The in-dock source toggle keeps using the reference reader.
const toggle = Array.from(document.querySelectorAll<HTMLButtonElement>("button.workspace-iconbtn"))
  .find(button => /^(source|源码|原始碼)$/i.test(button.getAttribute("aria-label") ?? ""));
if (!toggle) throw new Error("a text reference should expose the source toggle");
await act(async () => {
  toggle.dispatchEvent(new window.MouseEvent("click", { bubbles: true }));
  await flushPromises();
});
await waitFor("reference source", () => document.body.textContent?.includes("REFERENCE SOURCE") === true);

await act(async () => root.unmount());
dom.window.close();
console.log("workspace reference reader passed");
