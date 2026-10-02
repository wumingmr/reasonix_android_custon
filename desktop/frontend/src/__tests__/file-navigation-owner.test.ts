// Run: tsx src/__tests__/file-navigation-owner.test.ts
//
// The navigation instance is exercised in isolation: ports are plain functions,
// so every assertion is about identity, revision and lifetime rather than about
// any store, bridge or React runtime.

import assert from "node:assert/strict";
import { fileAccessContext, type FileResourceRef } from "../lib/fileResource";
import { FILE_PREVIEW_LIMIT, FileNavigationOwner, fileNavigationKey } from "../lib/fileNavigationOwner";

const DOCK = "dock-file";
const scope = { sessionTabId: "session-a", dockTabId: DOCK };
const key = fileNavigationKey(scope);
const resolutions = new Map<string, { resolve: (path: string) => void }>();
const owner = new FileNavigationOwner({
  resolve: (ref) => ({ hostId: ref.hostId, path: ref.path, identityPath: ref.path, requestedPath: ref.path, access: fileAccessContext(ref) }),
  revealDock: () => DOCK,
});
const presented = (path: string, toolCallId = "call"): FileResourceRef =>
  ({ source: "presented", hostId: "local", tabId: "session-a", toolCallId, path });
const workspace = (path: string): FileResourceRef =>
  ({ source: "workspace", hostId: "local", tabId: "session-a", path });
const snapshot = () => owner.getSnapshot(key);

// ── Identity: the same resource and mode is one entry, one revision ──
owner.open({ ref: presented("app.ts"), params: { action: "preview", view: "files" } });
const opened = snapshot()!;
assert.equal(opened.selected?.resource.path, "app.ts");
assert.equal(opened.entries.length, 1);
assert.equal(opened.sourcePaths.length, 0, "a preview entry is not a source entry");
const contentRevision = opened.contentRevision;
owner.open({ ref: presented("app.ts"), params: { action: "preview", view: "files" } });
assert.equal(snapshot()!.entries, opened.entries, "an equivalent entry keeps its list identity");
assert.equal(snapshot()!.selected, opened.selected, "an equivalent entry keeps its identity");
assert.equal(snapshot()!.contentRevision, contentRevision, "a repeat does not restart the read inputs");
assert.equal(snapshot()!.navigation!.revision, opened.navigation!.revision + 1, "a repeat is still delivered as its own navigation");

// Switching the mode reuses the tab and changes the read inputs.
owner.setSourceMode(scope, "app.ts", true);
assert.equal(snapshot()!.entries.length, 1, "switching to source reuses the preview tab");
assert.deepEqual(snapshot()!.sourcePaths, ["app.ts"]);
assert.notEqual(snapshot()!.contentRevision, contentRevision, "switching the mode asks for the other representation");
owner.setSourceMode(scope, "app.ts", false);
assert.deepEqual(snapshot()!.sourcePaths, [], "switching back clears the source mode");

// The same path from another entry point is the same resource, new credentials.
const presentedEntry = snapshot()!.selected;
owner.open({ ref: workspace("app.ts"), params: { action: "preview", view: "files" } });
assert.equal(snapshot()!.entries.length, 1, "one path is one preview tab");
assert.notEqual(snapshot()!.selected, presentedEntry, "a new access context is a new entry record");
assert.deepEqual(snapshot()!.selected!.resource.access, { source: "workspace", tabId: "session-a" });
assert.equal(snapshot()!.selected!.resource.access.toolCallId, undefined, "a workspace reference never inherits a presented tool scope");

// ── Tabs: recency, the cap, and the neighbour a close selects ──
for (const path of ["b.ts", "c.ts", "d.ts", "e.ts", "f.ts"]) {
  owner.open({ ref: workspace(path), params: { action: "preview", view: "files" } });
}
assert.equal(snapshot()!.entries.length, FILE_PREVIEW_LIMIT, "the preview tab list keeps its limit");
assert.deepEqual(snapshot()!.entries.map((entry) => entry.resource.path), ["b.ts", "c.ts", "d.ts", "e.ts", "f.ts"]);
owner.open({ ref: workspace("c.ts"), params: { action: "preview", view: "files" } });
assert.deepEqual(snapshot()!.entries.map((entry) => entry.resource.path), ["b.ts", "d.ts", "e.ts", "f.ts", "c.ts"], "reopening moves the entry to the most recent position");
owner.closeEntry(scope, "c.ts");
assert.deepEqual(snapshot()!.entries.map((entry) => entry.resource.path), ["b.ts", "d.ts", "e.ts", "f.ts"]);
assert.equal(snapshot()!.selected?.resource.path, "f.ts", "closing the selected tab selects the previous one");
owner.selectEntry(scope, "b.ts");
assert.equal(snapshot()!.selected?.resource.path, "b.ts");
owner.closeEntry(scope, "b.ts");
assert.equal(snapshot()!.selected?.resource.path, "f.ts", "closing an unselected tab keeps the selection");
const beforeUnknownClose = snapshot();
owner.closeEntry(scope, "missing.ts");
assert.equal(snapshot(), beforeUnknownClose, "closing a tab that is not open changes nothing");

// ── A scoped list drops the tabs but keeps the selection ──
owner.clearEntries(scope);
assert.deepEqual(snapshot()!.entries, []);
assert.equal(snapshot()!.selected?.resource.path, "f.ts", "a scoped list does not close the preview");
owner.clearSelection(scope);
assert.equal(snapshot()!.selected, null);
const cleared = snapshot();
owner.clearSelection(scope);
assert.equal(snapshot(), cleared, "clearing an empty selection produces no new snapshot");

// ── Restore carries workspace access only ──
owner.dispose();
const restoredOwner = new FileNavigationOwner({
  resolve: (ref) => ({ hostId: ref.hostId, path: ref.path, identityPath: ref.path, requestedPath: ref.path, access: fileAccessContext(ref) }),
  revealDock: () => DOCK,
});
restoredOwner.bindScope(scope, { resource: "project", session: "workspace-scope" });
restoredOwner.restore(scope, { paths: ["a.md", "b.md"], selectedPath: "b.md", hostId: "local" });
const restored = restoredOwner.getSnapshot(key)!;
assert.deepEqual(restored.entries.map((entry) => entry.resource.path), ["a.md", "b.md"]);
assert.equal(restored.selected?.resource.path, "b.md");
assert.deepEqual(restored.selected?.resource.access, { source: "workspace", tabId: "session-a" }, "a remembered path never restores a presented scope");
assert.equal(restored.navigation, null, "a restore is not a command and carries no navigation intent");
const restoredSnapshot = restoredOwner.getSnapshot(key);
restoredOwner.restore(scope, { paths: ["c.md"], selectedPath: null, hostId: "local" });
assert.equal(restoredOwner.getSnapshot(key), restoredSnapshot, "a restore never outranks a record a command or an earlier restore wrote");

// A slow restore is background hydration. An explicit command that begins
// afterward owns the dock even before its own path resolution completes.
const hydration = new Map<string, { resolve: (resource: ReturnType<typeof workspace>) => void }>();
const hydrationOwner = new FileNavigationOwner({
  resolve: (ref) => new Promise((resolve) => hydration.set(ref.path, {
    resolve: (resource) => resolve({
      hostId: resource.hostId,
      path: resource.path,
      identityPath: `/repo/${resource.path}`,
      requestedPath: resource.path,
      access: fileAccessContext(resource),
    }),
  })),
  revealDock: () => DOCK,
});
hydrationOwner.bindScope(scope, { resource: "project", session: "workspace-scope" });
const restoring = hydrationOwner.restore(scope, { paths: ["remembered.md"], selectedPath: "remembered.md", hostId: "local" });
const liveOpen = hydrationOwner.open({ ref: workspace("live.md"), params: { action: "preview", view: "files" } });
hydration.get("remembered.md")!.resolve(workspace("remembered.md"));
await restoring;
assert.equal(hydrationOwner.getSnapshot(key)!.selected, null, "late hydration cannot commit over a resolving command");
hydration.get("live.md")!.resolve(workspace("live.md"));
assert.equal((await liveOpen as { status: string }).status, "opened");
assert.equal(hydrationOwner.getSnapshot(key)!.selected?.resource.path, "live.md");

// ── Another session in the same project keeps the previews, not the scope ──
restoredOwner.open({ ref: presented("presented.md"), params: { action: "preview", view: "files" } });
const scopeSnapshot = restoredOwner.getSnapshot(key)!;
assert.equal(scopeSnapshot.selected?.resource.access.source, "presented");
restoredOwner.bindScope(scope, { resource: "project", session: "another-session-scope" });
const rescoped = restoredOwner.getSnapshot(key)!;
assert.equal(rescoped.signal.aborted, false, "another session in the same project keeps this dock's lifetime");
assert.deepEqual(rescoped.entries.map((entry) => entry.resource.path), ["a.md", "b.md", "presented.md"],
  "another session keeps what the dock was showing");
assert.equal(rescoped.generation, scopeSnapshot.generation, "another session is not a new lifecycle");
assert(rescoped.contentRevision > scopeSnapshot.contentRevision, "the previews are re-read under the new session");
assert.equal(rescoped.selected?.resource.access.source, "workspace", "a presented scope does not survive into another session");
assert.equal(rescoped.selected?.resource.access.toolCallId, undefined);
restoredOwner.bindScope(scope, { resource: "project", session: "another-session-scope" });
assert.equal(restoredOwner.getSnapshot(key), rescoped, "binding the same session again changes nothing");

// ── Another resource space replaces the record outright ──
restoredOwner.bindScope(scope, { resource: "other-project", session: "another-session-scope" });
const rescoped2 = restoredOwner.getSnapshot(key)!;
assert(rescoped.signal.aborted, "another project ends the previous lifetime");
assert.deepEqual(rescoped2.entries, [], "another project keeps no entries");
assert(rescoped2.generation > rescoped.generation, "a rebuilt record advances its generation");

restoredOwner.retain([fileNavigationKey({ sessionTabId: "session-a", dockTabId: "other-dock" })]);
assert.equal(restoredOwner.getSnapshot(key), null, "a dock that is no longer open keeps no record");
assert(rescoped.signal.aborted);
restoredOwner.bindScope(scope, { resource: "other-project", session: "another-session-scope" });
const reopened = restoredOwner.getSnapshot(key)!;
assert(reopened.generation > rescoped2.generation, "reopening the same dock tab id starts a new lifecycle generation");
assert.deepEqual(reopened.entries, [], "a lifecycle generation never restores the previous one's previews");
restoredOwner.open({ ref: workspace("b.md"), params: { action: "preview", view: "files" } });

// ── The dock instance names the record; the session only names its credentials ──
const otherDock = fileNavigationKey({ sessionTabId: "session-a", dockTabId: "dock-remote" });
assert.equal(fileNavigationKey({ sessionTabId: "session-b", dockTabId: DOCK }), key,
  "another session on the same dock is the same record");
assert.equal(restoredOwner.getSnapshot(otherDock), null, "another dock tab is another record");
restoredOwner.open({ ref: { source: "presented", hostId: "local", tabId: "session-b", toolCallId: "call", path: "other.ts" }, params: { action: "preview", view: "files" } });
const shared = restoredOwner.getSnapshot(key)!;
assert.equal(shared.selected?.resource.path, "other.ts", "a command from another session lands in the dock it targeted");
assert.deepEqual(shared.selected?.resource.access, { source: "presented", tabId: "session-b", toolCallId: "call" },
  "and carries that session's access context");

// ── A click is never replaced by an earlier command's late result ──
const slow = new FileNavigationOwner({
  resolve: (ref) => new Promise((resolve) => resolutions.set(ref.path, {
    resolve: (path) => resolve({ hostId: ref.hostId, path, identityPath: path, requestedPath: ref.path, access: fileAccessContext(ref) }),
  })),
  revealDock: () => DOCK,
});
const slowOpen = slow.open({ ref: workspace("slow.md"), params: { action: "preview", view: "files" } });
assert(resolutions.has("slow.md"), "the slow open is resolving");
const click = slow.selectPath(scope, { hostId: "local", path: "clicked.md" });
resolutions.get("clicked.md")!.resolve("clicked.md");
await click;
assert.equal(slow.getSnapshot(key)!.selected?.resource.path, "clicked.md", "the click lands while the open is still resolving");
resolutions.get("slow.md")!.resolve("slow.md");
assert.deepEqual(await slowOpen, { status: "cancelled", reason: "superseded" });
assert.equal(slow.getSnapshot(key)!.selected?.resource.path, "clicked.md", "the late result does not replace what the user clicked");

// ── A command that moves the dock to another host survives the panel's bind ──
const hostScoped = new FileNavigationOwner({
  resolve: (ref) => new Promise((resolve) => resolutions.set("host-b.md", {
    resolve: () => resolve({ hostId: ref.hostId, path: "host-b.md", identityPath: "host-b.md", requestedPath: ref.path, access: fileAccessContext(ref) }),
  })),
  revealDock: () => "dock-remote",
});
const remoteScope = { sessionTabId: "session-a", dockTabId: "dock-remote" };
hostScoped.bindScope(remoteScope, { resource: "host-a", session: "session-a" });
const toHostB = hostScoped.open({ ref: { source: "workspace", hostId: "host-b", tabId: "session-a", path: "host-b.md" }, params: { action: "preview", view: "files" } });
// The panel renders the host the command moved it to and binds that space.
hostScoped.bindScope(remoteScope, { resource: "host-b", session: "session-a" });
resolutions.get("host-b.md")!.resolve("host-b.md");
assert.equal((await toHostB as { status: string }).status, "opened", "the bind of the new host does not cancel the command that caused it");
assert.equal(hostScoped.getSnapshot("dock-remote")!.selected?.resource.path, "host-b.md");

// ── A panel acting on its own contents never picks a dock ──
let reveals = 0;
const panelOwner = new FileNavigationOwner({
  resolve: (ref) => ({ hostId: ref.hostId, path: ref.path, identityPath: ref.path, requestedPath: ref.path, access: fileAccessContext(ref) }),
  revealDock: () => { reveals += 1; return "another-dock"; },
});
panelOwner.openIn(scope, { ref: workspace("own.ts"), params: { action: "preview", view: "files" } });
assert.equal(reveals, 0, "a command inside a panel must not ask which dock to open");
assert.equal(panelOwner.getSnapshot(key)!.selected?.resource.path, "own.ts", "it commits to the dock the caller named");
assert.equal(panelOwner.getSnapshot(fileNavigationKey({ sessionTabId: "session-a", dockTabId: "another-dock" })), null);

// ── Canonical identity deduplicates caller spellings of the same file ──
const canonicalOwner = new FileNavigationOwner({
  resolve: (ref) => ({
    hostId: ref.hostId,
    path: ref.path,
    identityPath: ref.path.startsWith("/repo/") ? ref.path : `/repo/${ref.path}`,
    requestedPath: ref.path,
    access: fileAccessContext(ref),
  }),
  revealDock: () => DOCK,
});
canonicalOwner.open({ ref: workspace("src/a.ts"), params: { action: "preview", view: "files" } });
canonicalOwner.open({ ref: workspace("/repo/src/a.ts"), params: { action: "preview", view: "files" } });
assert.deepEqual(canonicalOwner.getSnapshot(key)!.entries.map((entry) => entry.resource.path), ["/repo/src/a.ts"],
  "relative and absolute spellings of one canonical file reuse a preview tab");

// ── A failed resolution reports to its caller and commits nothing ──
const failing = new FileNavigationOwner({
  resolve: () => { throw new Error("path not permitted"); },
  revealDock: () => DOCK,
});
assert.deepEqual(failing.open({ ref: workspace("secret.ts"), params: { action: "preview", view: "files" } }),
  { status: "failed", error: new Error("path not permitted") });
assert.equal(failing.getSnapshot(key)?.selected ?? null, null);

restoredOwner.dispose();
console.log("PASS file navigation identity, revisions, tabs, restore and lifetimes");
