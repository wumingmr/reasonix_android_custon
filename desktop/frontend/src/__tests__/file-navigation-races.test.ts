import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { openResource, performResourceAction } from "../lib/fileNavigationCommands";
import { createFileNavigationOwner, setFileNavigationOwner } from "../lib/fileNavigationCommands";
import { fileNavigationKey, type FileNavigationSnapshot } from "../lib/fileNavigationOwner";
import { useActivityBarStore } from "../store/activityBar";
import { useBrowserPanelStore } from "../lib/browserPanelStore";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
const dom = new JSDOM("", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document });
const paths = new Map<string, ReturnType<typeof deferred<string>>>();
const creations = new Map<string, ReturnType<typeof deferred<string>>>();
const revoked: string[] = [];
const stub = installDesktopHostStub({
  ResolveRemoteWorkspacePathForTab: (_tab: string, _host: string, _tool: string, path: string) => {
    const pending = deferred<string>(); paths.set(path, pending); return pending.promise;
  },
  CreatePresentedBrowserPreviewForTab: (_tab: string, _tool: string, path: string) => {
    if (path === "slow.html") { const pending = deferred<string>(); creations.set(path, pending); return pending.promise; }
    return Promise.resolve(`http://preview.test/${path}`);
  },
  RevokeWorkspaceBrowserPreview: async (url: string) => { revoked.push(url); },
});
const owner = createFileNavigationOwner();
setFileNavigationOwner(owner);
// A remote reference targets the remote dock; a local one targets the file dock.
const dockTabId = useActivityBarStore.getState().openEntry("remote", "Remote");
const fileDockTabId = useActivityBarStore.getState().openEntry("file", "Files");
const ref = { hostId: "remote", tabId: "session", source: "workspace" as const, toolCallId: "tool" };
const snapshot = (): FileNavigationSnapshot | null =>
  owner.getSnapshot(fileNavigationKey({ sessionTabId: "session", dockTabId }));

// A resolves after B: the late result of the superseded command must not win.
const first = performResourceAction({ ...ref, path: "first" }, "preview");
const second = performResourceAction({ ...ref, path: "second" }, "source");
paths.get("second")!.resolve("/second"); await second;
const current = snapshot();
paths.get("first")!.resolve("/first"); await first;
assert.equal(snapshot(), current, "a superseded resolution must not produce a new snapshot");
assert.equal(current?.selected?.resource.path, "/second");
assert.equal(current?.selected?.source, true);

// Closing the target dock ends the record: a late rejection is a cancellation,
// not a failure reported back to the row that asked.
const cancelled = performResourceAction({ ...ref, path: "cancelled" }, "preview");
owner.retain([fileNavigationKey({ sessionTabId: "session", dockTabId: fileDockTabId })]);
paths.get("cancelled")!.reject(new Error("obsolete failure"));
assert.deepEqual(await cancelled, { status: "cancelled", reason: "superseded" });
assert.equal(snapshot(), null, "a closed dock keeps no record to restore");

// A resolution that lands after its dock was closed is a cancellation too: the
// record it belonged to is gone, and nothing restores it.
const switched = performResourceAction({ ...ref, path: "switched" }, "preview");
owner.retain([fileNavigationKey({ sessionTabId: "session", dockTabId: fileDockTabId })]);
paths.get("switched")!.resolve("/switched");
assert.deepEqual(await switched, { status: "cancelled", reason: "superseded" });
assert.equal(snapshot(), null, "a closed dock leaves no record behind");

const opened = deferred<{ id: string }>();
const opening = deferred<void>();
const closed: string[] = [];
// The first call is held open so the dock can close mid-flight; later calls
// answer immediately with their own tab, the way a real host does.
let hostOpens = 0;
const host = {
  open: () => {
    if (hostOpens++ > 0) return Promise.resolve({ id: `open-tab-${hostOpens}` });
    opening.resolve();
    return opened.promise;
  },
  close: async (id: string) => { closed.push(id); },
};
useBrowserPanelStore.setState({ host: host as unknown as NonNullable<ReturnType<typeof useBrowserPanelStore.getState>["host"]> });
const browser = performResourceAction({ hostId: "local", tabId: "session", source: "presented", toolCallId: "tool", path: "one.html" }, "browser");
await opening.promise;
owner.retain([]);
opened.resolve({ id: "only-owned-tab" });
assert.deepEqual(await browser, { status: "cancelled", reason: "superseded" });
assert.deepEqual(revoked, ["http://preview.test/one.html"], "a URL created after its dock closed is revoked once");
assert(!useBrowserPanelStore.getState().tabs.some(tab => tab.id === "only-owned-tab"));
assert.deepEqual(closed, ["only-owned-tab"]);

// A preview URL minted before its operation lost the dock is released too: a
// second browser command supersedes the first while its creation is in flight.
const slow = performResourceAction({ hostId: "local", tabId: "session", source: "presented", toolCallId: "tool", path: "slow.html" }, "browser");
for (let tick = 0; tick < 20 && !creations.has("slow.html"); tick += 1) await new Promise((resolve) => setTimeout(resolve, 0));
assert(creations.has("slow.html"), "the first preview creation is in flight");
const fast = performResourceAction({ hostId: "local", tabId: "session", source: "presented", toolCallId: "tool", path: "fast.html" }, "browser");
creations.get("slow.html")!.resolve("http://preview.test/slow");
assert.deepEqual(await slow, { status: "cancelled", reason: "superseded" });
assert.deepEqual(revoked, ["http://preview.test/one.html", "http://preview.test/slow"],
  "a URL created after its operation lost the dock is revoked");
await fast.catch(() => undefined);

// A host that never arrives must not open a page, and its URL is released.
useBrowserPanelStore.setState({ host: null });
const waiting = performResourceAction({ hostId: "local", tabId: "session", source: "presented", toolCallId: "tool", path: "two.html" }, "browser");
await new Promise((resolve) => setTimeout(resolve, 2200));
assert.deepEqual(await waiting, { status: "failed", error: new Error("Built-in browser is not ready") });
assert.deepEqual(revoked, ["http://preview.test/one.html", "http://preview.test/slow", "http://preview.test/two.html"], "every preview URL this session minted is released exactly once");
const openTabs = useBrowserPanelStore.getState().tabs;
assert.deepEqual(openTabs.map((tab) => tab.id), ["open-tab-2"], "only the preview whose host arrived opened a page");
assert(!openTabs.some((tab) => tab.id === "only-owned-tab"), "a tab from a dock that closed never opens");

// Direct bridge actions keep the public outcome contract even when the host
// rejects: callers receive `failed` instead of an escaping promise rejection.
Object.assign(stub.commands, {
  OpenWorkspacePathForTab: async () => { throw new Error("open denied"); },
  RevealWorkspacePathForTab: async () => { throw new Error("reveal denied"); },
  SaveWorkspacePathAsForTab: async () => { throw new Error("save denied"); },
});
for (const [action, message] of [
  ["open-native", "open denied"],
  ["reveal-native", "reveal denied"],
  ["save-copy", "save denied"],
] as const) {
  const outcome = await performResourceAction(
    { hostId: "local", tabId: "session", source: "workspace", path: "failed.txt" },
    action,
  );
  assert.equal(outcome.status, "failed");
  assert.equal((outcome as { error: Error }).error.message, message);
}

// Answer-named references join the same owner, but preserve their dedicated
// host revalidation path for navigation and every direct action.
const referenceCalls: string[] = [];
Object.assign(stub.commands, {
  ResolveReferencePathForTab: async (_tab: string, path: string) => {
    referenceCalls.push(`resolve:${path}`);
    return `/repo/${path}`;
  },
  OpenReferencePathForTab: async (_tab: string, path: string) => { referenceCalls.push(`open:${path}`); },
  RevealReferencePathForTab: async (_tab: string, path: string) => { referenceCalls.push(`reveal:${path}`); },
  SaveReferencePathAsForTab: async (_tab: string, path: string) => { referenceCalls.push(`save:${path}`); return `/copy/${path}`; },
});
const reference = { hostId: "local", tabId: "session", source: "reference" as const, path: "answer.md" };
const referenceOpen = await performResourceAction(reference, "preview");
assert.equal(referenceOpen.status, "opened");
assert.equal(referenceOpen.status === "opened" ? referenceOpen.resource.identityPath : "", "/repo/answer.md");
const referenceSnapshot = owner.getSnapshot(fileNavigationKey({ sessionTabId: "session", dockTabId: fileDockTabId }));
assert.equal(referenceSnapshot?.selected?.resource.access.source, "reference");
for (const action of ["open-native", "reveal-native", "save-copy"] as const) {
  assert.equal((await performResourceAction(reference, action)).status, "opened");
}
assert.deepEqual(referenceCalls, ["resolve:answer.md", "open:answer.md", "reveal:answer.md", "save:answer.md"]);

// Default preview routing is centralized: every local HTML entry point asks
// the shared backend service for a task-owned browser tab.
const sharedPreviewCalls: Array<{ tabId: string; source: string; path: string; userInitiated?: boolean }> = [];
const taskTab = {
  id: "task-html", taskId: "session", url: "http://preview.test/index.html", title: "index.html",
  loading: false, canGoBack: false, canGoForward: false, temporary: false,
  mode: "agent" as const, epoch: 0, zoom: 1, error: null,
};
Object.assign(stub.commands, {
  OpenFileBrowserPreviewForTab: async (tabId: string, request: { source: string; path: string; userInitiated?: boolean }) => {
    sharedPreviewCalls.push({ tabId, source: request.source, path: request.path, userInitiated: request.userInitiated });
    return { tabId: taskTab.id, url: taskTab.url, status: "opened", sessionGeneration: 1 };
  },
});
useBrowserPanelStore.setState({
  host: {
    list: async () => [taskTab], activate: async () => {}, close: async () => {},
  } as unknown as NonNullable<ReturnType<typeof useBrowserPanelStore.getState>["host"]>,
  tabs: [], taskId: "session",
});
const htmlDefault = await openResource({ hostId: "local", tabId: "session", source: "workspace", path: "INDEX.HTML" }, { view: "preview" });
assert.equal(htmlDefault.status, "opened");
assert.deepEqual(sharedPreviewCalls, [{ tabId: "session", source: "workspace", path: "INDEX.HTML", userInitiated: false }]);
assert.equal(useBrowserPanelStore.getState().activeTabId, taskTab.id);
stub.uninstall(); dom.window.close();
console.log("PASS navigation ordering, cancellation, failed outcomes and browser resource cleanup");
