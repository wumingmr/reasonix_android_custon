// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/project-tree-loading.test.tsx
import assert from "node:assert/strict";
import { mock } from "node:test";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import type { Root } from "react-dom/client";
import type { ReasonixDesktopHost } from "../lib/desktopHost";
import type { ProjectNode, ProjectTopicPage, ProjectTopicPageRequest, SessionGroup } from "../lib/types";

const dom = new JSDOM('<html><body><div id="root"></div></body></html>', { url: "http://localhost/", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, Element: dom.window.Element,
  HTMLElement: dom.window.HTMLElement, Node: dom.window.Node, Event: dom.window.Event,
  MouseEvent: dom.window.MouseEvent, localStorage: dom.window.localStorage,
  requestAnimationFrame: (callback: FrameRequestCallback) => setTimeout(() => callback(Date.now()), 0),
  cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
  IS_REACT_ACT_ENVIRONMENT: true,
});
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
window.matchMedia = (() => ({ matches: false, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;

// Import the event system after installing the DOM, so real React input events
// exercise the component's search handler as they do in the renderer.
const { createRoot } = await import("react-dom/client");
const { ProjectTree } = await import("../components/ProjectTree");
const { LocaleProvider } = await import("../lib/i18n");
const { ToastProvider } = await import("../lib/toast");
const { resetProjectTreeRuntimeWindowLimits } = await import("../lib/projectTreeWindow");

const roots = ["/review-a", "/review-b"];
const projects: ProjectNode[] = roots.map((root, i) => ({ key: `project-${i}`, kind: "project", label: i ? "B" : "A", root, children: [] }));
let visibleProjects = projects;
const releasedSnapshots: string[] = [];
const topic = (id: string, root = roots[0]): ProjectNode => ({ key: id, topicId: id, kind: "topic", label: id, root, children: [] });
const listeners = new Map<string, Set<(...args: unknown[]) => void>>();
let revision = 1;
let mountID = 0;
let rows: Record<string, ProjectNode[]> = {};
let groups: SessionGroup[] = [];
let calls: ProjectTopicPageRequest[] = [];
const removedWorkspaces: string[] = [];
const renamedSessions: string[] = [];
let intercept: ((req: ProjectTopicPageRequest) => Promise<ProjectTopicPage> | undefined) | undefined;
let snapshotFailure: Error | null = null;
const catalog = () => ({ state: "ready", revision, indexed: 2, total: 2, repairPending: 0 });

function page(req: ProjectTopicPageRequest): ProjectTopicPage {
  const query = req.query?.toLowerCase() ?? "";
  const selected = req.groupId ? [] : (rows[req.workspaceRoot ?? ""] ?? []).filter(row => row.label.toLowerCase().includes(query));
  const start = Number(req.cursor || 0), limit = req.limit ?? 5;
  const items = selected.slice(start, start + limit);
  return { revision, items, complete: true, nextCursor: start + items.length < selected.length ? String(start + items.length) : undefined };
}
const bindings = {
  ArchiveSessionTarget: async ({ sessionPath }: { sessionPath: string }) => {
    rows = Object.fromEntries(Object.entries(rows).map(([key, items]) => [key, items.filter(row => row.sessionPath !== sessionPath)]));
    revision++;
    return { committed: true, lifecycleGeneration: 1, operationId: `archive-${sessionPath}` };
  },
  GetProjectTreeSnapshot: async () => {
    if (snapshotFailure) throw snapshotFailure;
    return { revision, projects: visibleProjects, catalog: catalog() };
  },
  ReleaseReadSnapshot: async (id: string) => { releasedSnapshots.push(id); },
  ListProjectTopics: async (req: ProjectTopicPageRequest) => { calls.push(req); return intercept?.(req) ?? page(req); },
  GetSessionCatalogStatus: async () => catalog(),
  GetSessionOrganization: async () => ({ groups, revision, order: [], manualOrderEnabled: false }),
  GetProjectTreeRuntimeSnapshot: async () => ({ revision: 0, topics: [] }),
  IsolatedWorktreeAvailability: async () => ({ available: true, reason: "" }),
  RemoveWorkspace: async (path: string) => { removedWorkspaces.push(path); },
  RenameSessionTarget: async (_target: unknown, title: string) => { renamedSessions.push(title); },
  Platform: async () => "darwin",
  RemoteConnectionStatuses: async () => [],
};
window.reasonixDesktop = {
  kind: "electron", contract: { commands: Object.keys(bindings) }, platform: { os: "darwin" }, native: {},
  invoke: (method: string, args: unknown[]) => (bindings as unknown as Record<string, (...args: unknown[]) => Promise<unknown>>)[method](...args),
  on: (name: string, cb: (...args: unknown[]) => void) => {
    const set = listeners.get(name) ?? new Set(); set.add(cb); listeners.set(name, set);
    return () => set.delete(cb);
  },
} as unknown as ReasonixDesktopHost;

const container = document.getElementById("root")!;
let root: Root;
const flush = async () => { await act(async () => { await new Promise<void>(resolve => setImmediate(resolve)); }); };
const advance = async (ms = 200) => { await act(async () => mock.timers.tick(ms)); await flush(); };
const labels = () => [...container.querySelectorAll(".project-tree__topic-label")].map(el => el.textContent);
const count = (workspaceRoot = roots[0]) => calls.filter(req => req.workspaceRoot === workspaceRoot).length;
async function click(selector: string, scope: ParentNode = container) {
  const target = scope.querySelector<HTMLElement>(selector);
  assert.ok(target, `missing control: ${selector}`);
  await act(async () => target.click()); await flush();
}
async function folder(label = "A") {
  const target = [...container.querySelectorAll<HTMLElement>(".project-tree__folder--project .project-tree__folder-main")]
    .find(el => el.textContent?.trim() === label);
  assert.ok(target, `missing project ${label}`);
  await act(async () => target.click()); await flush();
}
async function event(stale = false, reason = "changed") {
  revision++;
  await act(async () => {
    for (const callback of listeners.get("project-tree:changed-v2") ?? []) callback({ revision: stale ? 0 : revision, roots: [roots[0]], reason });
  });
	await advance(500);
}
async function search(value: string) {
  const input = container.querySelector<HTMLInputElement>(".project-tree__search input")!;
  assert.ok(input, "search input exists");
  await act(async () => {
    Object.getOwnPropertyDescriptor(dom.window.HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  });
  await flush();
}
async function mount(withGroups = false, withSessions = false, onTopicsChanged?: () => Promise<void>) {
  mountID++;
  revision = 1; calls = []; intercept = undefined; snapshotFailure = null; visibleProjects = projects; releasedSnapshots.length = 0; removedWorkspaces.length = 0;
  rows = Object.fromEntries(roots.map((path, i) => [path, Array.from({ length: 12 }, (_, n) => topic(`${i ? "B" : "A"}-${n}`, path))]));
  if (withSessions) rows = Object.fromEntries(Object.entries(rows).map(([path, items]) => [path, items.map(row => ({ ...row, kind: "session", sessionPath: `/sessions/${mountID}/${row.key}` }))]));
  groups = withGroups ? [{ id: "feature", title: "Feature", topicIds: [] }] : [];
  resetProjectTreeRuntimeWindowLimits(); localStorage.clear();
  root = createRoot(container);
  await act(async () => root.render(<LocaleProvider><ToastProvider><ProjectTree activeScope="project" activeWorkspaceRoot={roots[0]} onOpenTopic={() => {}} onAddProject={async () => {}} onTopicsChanged={onTopicsChanged} /></ToastProvider></LocaleProvider>));
  await flush(); await advance();
}
async function unmount() { await act(async () => root.unmount()); }
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

mock.timers.enable({ apis: ["setTimeout", "Date"] });
try {
  await mount(false, true);
  await act(async () => container.querySelector(".project-tree__topic-main")!.dispatchEvent(new dom.window.MouseEvent("contextmenu", { bubbles: true, clientX: 30, clientY: 30 })));
  await flush();
  const renameSession = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(button => button.textContent?.includes("Rename session"));
  assert.ok(renameSession, "session context menu offers Rename session");
  await act(async () => renameSession.click()); await flush();
  const renameInput = container.querySelector<HTMLInputElement>(".project-tree__topic-input");
  assert.ok(renameInput, "session rename opens the inline input");
  const composingEnter = new dom.window.KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true });
  const safariEnter = new dom.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true });
  Object.defineProperty(safariEnter, "keyCode", { value: 229 });
  await act(async () => { renameInput.dispatchEvent(composingEnter); renameInput.dispatchEvent(safariEnter); });
  assert.equal(container.querySelector(".project-tree__topic-input"), renameInput, "IME Enter keeps the session rename editor open");
  assert.deepEqual(renamedSessions, [], "IME Enter does not persist a partial session name");
  await act(async () => renameInput.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  await flush();
  assert.equal(container.querySelector(".project-tree__topic-input"), null, "ordinary Enter finishes session rename");
  assert.equal(renamedSessions.length, 1, "ordinary Enter persists the session name once");
  await unmount();
  console.log("  PASS  session rename ignores IME Enter and accepts ordinary Enter");

  await mount(true);
  const coldPage = deferred<ProjectTopicPage>();
  intercept = req => req.workspaceRoot === roots[1] ? coldPage.promise.then(() => page(req)) : undefined;
  await folder("B");
  assert.ok(container.querySelector(".project-tree__skeleton, .project-tree__topic-window-status"), "first expansion still indicates a pending initial page");
  await act(async () => coldPage.resolve(page(calls.at(-1)!))); await flush();
  intercept = undefined;
  await click('[aria-label="Show more in A"]');
  assert.equal(labels().filter(label => label?.startsWith("A-")).length, 10);
  for (let turn = 0; turn < 3; turn++) {
    const refreshPage = deferred<ProjectTopicPage>();
    intercept = req => req.workspaceRoot === roots[0] ? refreshPage.promise.then(() => page(req)) : undefined;
    const residentRows = [...container.querySelectorAll(".project-tree__topic-main")];
    const more = container.querySelector<HTMLButtonElement>('[aria-label="Show more in A"]')!;
    await event();
    assert.equal(container.querySelectorAll(".project-tree__topic-window-status, .project-tree__skeleton").length, 0,
      "turn refreshes keep both the populated list and its empty group quiet");
    assert.equal(container.querySelector('[aria-label="Show more in A"]'), more, "background refresh retains the footer control instead of swapping it for loading text");
    assert.equal(more.disabled, true, "the retained control cannot mix cursors while refreshing");
    assert.deepEqual([...container.querySelectorAll(".project-tree__topic-main")], residentRows, "refresh start retains existing row elements");
    assert.equal(calls.filter(req => req.workspaceRoot === roots[0] && !req.groupId).at(-1)?.limit, 10, "refresh retains the expanded window");
    rows[roots[0]] = rows[roots[0]].map((row, i) => i === 0 ? { ...row, label: `A-updated-${turn}` } : row);
    await act(async () => refreshPage.resolve(page({ workspaceRoot: roots[0], limit: 10 }))); await flush();
    assert.ok(labels().includes(`A-updated-${turn}`), "background updates still reach the rendered row");
    assert.deepEqual([...container.querySelectorAll(".project-tree__topic-main")], residentRows, "settlement reuses stable row elements");
    assert.equal(labels().filter(label => label?.startsWith("A-")).length, 10);
    assert.equal(more.disabled, false);
  }
  const appendPage = deferred<ProjectTopicPage>();
  intercept = req => req.workspaceRoot === roots[0] ? appendPage.promise : undefined;
  await click('[aria-label="Show more in A"]');
  assert.equal(container.querySelectorAll(".project-tree__topic-window-status").length, 1, "an explicit next page still shows loading");
  await act(async () => appendPage.resolve(page(calls.at(-1)!))); await flush();
  assert.equal(labels().filter(label => label?.startsWith("A-")).length, 12);
  const failedRefresh = deferred<ProjectTopicPage>();
  intercept = req => req.workspaceRoot === roots[0] && !req.groupId ? failedRefresh.promise : undefined;
  await event();
  await act(async () => failedRefresh.reject(new Error("catalog temporarily unavailable"))); await flush();
  assert.ok(container.querySelector('[aria-label="Retry loading A"]'), "silent reads still expose failures and retry");
  assert.equal(labels().filter(label => label?.startsWith("A-")).length, 12, "refresh failure retains the resident page");
  intercept = undefined;
  await click('[aria-label="Retry loading A"]');
  await unmount();
  console.log("  PASS  background refresh is silent, preserves expanded rows, and keeps foreground/error feedback");

  for (const remaining of [0, 1]) {
    await mount();
    rows[roots[0]] = rows[roots[0]].slice(0, remaining);
    await event();
    const exhaustedRefresh = deferred<ProjectTopicPage>();
    intercept = () => exhaustedRefresh.promise;
    await event();
    assert.equal(labels().length, remaining, "an exhausted list retains its resident rows during refresh");
    assert.equal(container.querySelectorAll(".project-tree__skeleton, .project-tree__topic-window-status").length, 0,
      "a settled exhausted folder refreshes without a loading placeholder");
    const exhaustedPage = page(calls.at(-1)!);
    assert.equal(exhaustedPage.nextCursor, undefined);
    await act(async () => exhaustedRefresh.resolve(exhaustedPage)); await flush();
    const settledCalls = count();
    await advance(360_000);
    assert.equal(count(), settledCalls, "an exhausted list does not keep requesting nonexistent pages");
    assert.equal(labels().length, remaining);
    assert.equal(container.querySelectorAll(".project-tree__skeleton, .project-tree__topic-window-status").length, 0);
    assert.equal(container.querySelector('[aria-label="Show more in A"]'), null);
    await unmount();
  }
  console.log("  PASS  empty and single-row exhausted pages refresh quietly and stop loading");

  await mount();
  const stamped = (row: ProjectNode, at: number): ProjectNode => ({ ...row, createdAt: at, lastActivityAt: at });
  rows[roots[0]] = rows[roots[0]].map((row, n) => stamped(row, 1_000 - n));
  await event();
  const residentWindow = labels().filter(label => label?.startsWith("A-"));
  assert.deepEqual(residentWindow, ["A-0", "A-1", "A-2", "A-3", "A-4"], "a busy folder starts from a complete five-row window");
  rows[roots[0]] = [stamped(topic("A-fresh"), 2_000), ...rows[roots[0]]];
  intercept = req => req.workspaceRoot === roots[0] ? Promise.resolve({ ...page(req), complete: false }) : undefined;
  await event();
  assert.deepEqual(labels().filter(label => label?.startsWith("A-")), ["A-fresh", ...residentWindow.slice(0, 4)],
    "a conversation discovered while the catalog is incomplete is accepted into the window");
  intercept = undefined;
  await unmount();
  console.log("  PASS  an incomplete refresh keeps a new conversation visible in a busy folder");

  await mount();
  rows[roots[0]] = Array.from({ length: 70 }, (_, i) => topic(`Search-${i}`));
  await search("Search-"); await advance();
  const searchRefresh = deferred<ProjectTopicPage>();
  intercept = req => req.query ? searchRefresh.promise : undefined;
  await event();
  const searchMore = container.querySelector<HTMLButtonElement>('[aria-label="Load more results · A"]')!;
  assert.ok(searchMore);
  assert.equal(searchMore.textContent, "Load more results", "search refresh keeps the pagination label stable");
  assert.equal(searchMore.disabled, true);
  await act(async () => searchRefresh.resolve(page(calls.at(-1)!))); await flush();
  const searchAppend = deferred<ProjectTopicPage>();
  intercept = () => searchAppend.promise;
  await click('[aria-label="Load more results · A"]');
  assert.equal(searchMore.textContent, "Loading…", "explicit search pagination has visible loading feedback");
  await act(async () => searchAppend.resolve(page(calls.at(-1)!))); await flush();
  assert.equal(labels().length, 70);
  await unmount();
  console.log("  PASS  search results distinguish background refresh from explicit pagination");

  const archiveCases = [12, 2, 1].flatMap(size =>
    (["old-first", "fresh-first", "old-error"] as const).map(completion => ({ size, completion })));
  for (const { size, completion } of archiveCases) {
    await mount(false, true);
    rows[roots[0]] = rows[roots[0]].slice(0, size);
    await event();
    await folder("B");
    const oldRequest = deferred<ProjectTopicPage>(), freshRequest = deferred<ProjectTopicPage>();
    let requestIndex = 0;
    intercept = req => req.workspaceRoot === roots[0]
      ? (++requestIndex === 1 ? oldRequest.promise : freshRequest.promise) : undefined;
    await event();
    const oldPage = { ...page(calls.at(-1)!), snapshotId: `pre-archive-${completion}` };
    await act(async () => container.querySelector('.project-tree__topic-main')!.dispatchEvent(new dom.window.MouseEvent("contextmenu", { bubbles: true, clientX: 30, clientY: 30 })));
    await flush();
    await click('[role="menuitem"].context-menu__item--danger', document);
    await click('[role="menuitem"].context-menu__item--danger', document);
    assert.equal(labels().filter(label => label?.startsWith("A-")).length, Math.min(size, 5) - 1, "the committed archive removes one visible row immediately");
    await advance(500);
    if (completion !== "fresh-first") {
      await act(async () => completion === "old-error" ? oldRequest.reject(new Error("obsolete read failed")) : oldRequest.resolve(oldPage));
      await flush();
    }
    const freshPage = { ...page({ workspaceRoot: roots[0], limit: 5 }), snapshotId: `post-archive-${completion}` };
    await act(async () => freshRequest.resolve(freshPage));
    await flush(); await advance(500);
    assert.equal(container.querySelectorAll(".project-tree__topic-window-status").length, 0,
      "archiving during an in-flight page must finish loading after the replacement page settles");
    assert.deepEqual(labels().filter(label => label?.startsWith("A-")), rows[roots[0]].slice(0, 5).map(row => row.label), "replacement reads replenish the window or settle at the remaining row count");
    if (completion === "fresh-first") {
      await act(async () => oldRequest.resolve(oldPage)); await flush();
      assert.ok(!labels().includes("A-0"), "a late pre-archive response cannot resurrect the archived session");
    }
    assert.equal(count(roots[1]), 1, "archiving A leaves the sibling project's cache intact");
    if (completion !== "old-error") assert.ok(releasedSnapshots.includes(oldPage.snapshotId), "the discarded read releases its snapshot");
    intercept = req => req.workspaceRoot === roots[0] ? Promise.resolve({ ...page(req), snapshotId: freshPage.snapshotId }) : undefined;
    if (size > 5) {
      await click('[aria-label="Show more in A"]');
      assert.equal(labels().filter(label => label?.startsWith("A-")).length, 10, "pagination remains usable after archive");
    } else {
      assert.equal(container.querySelector('[aria-label="Show more in A"]'), null, "an exhausted archive result has no next-page control");
      const settledCalls = count();
      await advance(360_000);
      assert.equal(count(), settledCalls, "archiving to one or zero rows does not leave an automatic loading loop");
      assert.equal(container.querySelectorAll(".project-tree__skeleton, .project-tree__topic-window-status").length, 0);
    }
    await unmount();
  }
  console.log("  PASS  session archive retires pending pages for either completion order and obsolete errors");

  await mount();
  assert.equal(count(), 1, "cold first page loads once");
  await folder("B");
  assert.equal(count(roots[1]), 1);
  await folder(); await folder(); await advance();
  assert.equal(count(), 1, "unchanged project reopen reuses its cache");
  assert.equal(count(roots[1]), 1, "sibling expand/collapse never reloads B");
  await click('[aria-label="Collapse all"]');
  await click('[aria-label="Restore previous groups"]'); await advance();
  assert.equal(calls.length, 2, "unchanged global restore reuses both caches");

  await folder();
  rows[roots[0]] = [topic("A-new"), ...rows[roots[0]].filter(row => row.topicId !== "A-0")];
  await event();
  assert.equal(count(), 1, "collapsed invalidation does not eagerly fetch");
  await folder(); await advance();
  assert.equal(count(), 2, "reopening an invalidated project fetches once");
  assert.ok(labels().includes("A-new"));
  assert.ok(!labels().includes("A-0"), "externally archived row is removed");
  assert.equal(count(roots[1]), 1, "A's invalidation leaves B's cache valid");

  await click('[aria-label="Collapse all"]');
  rows[roots[0]] = [topic("A-newer"), ...rows[roots[0]].filter(row => row.topicId !== "A-new")];
  await event();
  assert.equal(count(), 2);
  await click('[aria-label="Restore previous groups"]'); await advance();
  assert.equal(count(), 3, "global restore reloads only invalidated A");
  assert.equal(count(roots[1]), 1);
  assert.ok(labels().includes("A-newer")); assert.ok(!labels().includes("A-new"));
  await folder();
  rows[roots[0]] = [topic("A-reconciled"), ...rows[roots[0]]];
  await event(true);
  await folder(); await advance();
  assert.ok(labels().includes("A-reconciled"), "out-of-order events still invalidate cached lists while reconciling shells");
  assert.equal(count(), 4); assert.equal(count(roots[1]), 1);
  await unmount();
  console.log("  PASS  project/global reopen caches, deferred invalidation and sibling isolation");

  await mount();
  rows[roots[0]] = [topic("Fork one"), ...rows[roots[0]]];
  await event(false, "membership");
  assert.ok(labels().includes("Fork one"), "the first fork enters the visible topic page");
  rows[roots[0]] = [topic("Fork two"), ...rows[roots[0]]];
  await event(false, "membership");
  assert.ok(labels().includes("Fork one") && labels().includes("Fork two"), "a second fork from the original appears without restarting");
  await unmount();
  console.log("  PASS  repeated fork membership events refresh the visible topic page");

  await mount(true);
  assert.deepEqual(calls.map(req => req.groupId || "ungrouped").sort(), ["feature", "ungrouped"], "group and parent initialization share one request per list");
  await advance();
  assert.equal(calls.length, 2, "no delayed duplicate first page");
  await folder(); await folder(); await advance();
  assert.equal(calls.length, 2, "reopening a grouped project reuses both lists");
  await unmount();
  console.log("  PASS  grouped cold start requests each list exactly once");

  await mount();
  const oldRequest = deferred<ProjectTopicPage>(), freshRequest = deferred<ProjectTopicPage>();
  let requestIndex = 0;
  intercept = () => (++requestIndex === 1 ? oldRequest.promise : freshRequest.promise);
  await event();
  const oldPage = page(calls.at(-1)!);
  rows[roots[0]] = [topic("A-fresh"), ...rows[roots[0]]];
  await event();
  assert.equal(count(), 2, "background events queue behind an active logical read");
  await act(async () => oldRequest.resolve(oldPage)); await flush();
  await advance(500);
  assert.equal(count(), 3, "queued refresh starts after the old read completes");
  const freshPage = page(calls.at(-1)!);
  await act(async () => freshRequest.resolve(freshPage)); await flush();
  assert.ok(labels().includes("A-fresh"));
  assert.equal(count(), 3);
  await unmount();
  console.log("  PASS  background events do not starve pending reads and coalesce into one follow-up");

  await mount();
  await folder();
  await search("A-");
  assert.equal(count(), 1, "typing is debounced");
  // An event is allowed to populate a visible search before debounce expires.
  rows[roots[0]] = [topic("A-search-new"), ...rows[roots[0]]];
  await event();
  assert.equal(count(), 2, "search visibility refreshes even a manually collapsed project");
  await advance();
  assert.equal(count(), 2, "debounced search reuses the event's initialized page");
  assert.ok(labels().includes("A-search-new"));
  const oldSearch = deferred<ProjectTopicPage>();
  intercept = req => req.query === "pending" ? oldSearch.promise : undefined;
  await search("pending"); await advance();
  await search("");
  await folder();
  await act(async () => oldSearch.resolve({ revision, items: [topic("stale-search")], complete: true })); await flush();
  assert.ok(labels().includes("A-search-new")); assert.ok(!labels().includes("stale-search"));
  assert.equal(container.querySelectorAll(".project-tree__topic-window-status").length, 0, "abandoned query cannot strand the normal list in loading");
  await unmount();
  console.log("  PASS  search visibility, debounce deduplication and abandoned requests");

  await mount();
  const initialSort = calls.at(-1)?.sortMode ?? "created";
  const nextSort = initialSort === "created" ? "updated" : "created";
  const oldSort = deferred<ProjectTopicPage>();
  intercept = req => req.sortMode === initialSort ? oldSort.promise : undefined;
  await event();
  const oldSortPage = page(calls.at(-1)!);
  await click('.project-tree__header-menu-wrap button[aria-haspopup="menu"]');
  const nextSortLabel = nextSort === "created" ? "Created time" : "Updated time";
  const nextSortButton = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(button => button.textContent?.includes(nextSortLabel));
  assert.ok(nextSortButton);
  await act(async () => nextSortButton.click()); await flush();
  assert.equal(calls.at(-1)?.sortMode, nextSort, "sorting starts a new request while the old order is pending");
  assert.equal(calls.at(-1)?.cursor, "", "new sort discards the old order's pagination cursor");
  assert.equal(count(), 3);
  await act(async () => oldSort.resolve(oldSortPage)); await flush();
  assert.equal(container.querySelectorAll(".project-tree__topic-window-status").length, 0);
  assert.equal(count(), 3);
  await unmount();
  console.log("  PASS  sort changes retire pending requests and their cursors");

  await mount();
  const removedRequest = deferred<ProjectTopicPage>();
  intercept = req => req.workspaceRoot === roots[0] ? removedRequest.promise : undefined;
  await event();
  visibleProjects = [projects[1]]; revision++;
  await act(async () => root.render(<LocaleProvider><ProjectTree activeScope="project" activeWorkspaceRoot={roots[1]} refreshSignal={1} onOpenTopic={() => {}} onAddProject={async () => {}} /></LocaleProvider>));
  await flush();
  assert.ok(!container.textContent?.includes("A-0"));
  visibleProjects = projects; revision++;
  intercept = undefined;
  rows[roots[0]] = [topic("A-returned")];
  await act(async () => root.render(<LocaleProvider><ProjectTree activeScope="project" activeWorkspaceRoot={roots[0]} refreshSignal={2} onOpenTopic={() => {}} onAddProject={async () => {}} /></LocaleProvider>));
  await flush(); await advance(500);
  assert.ok(labels().includes("A-returned"));
  await event();
  await act(async () => removedRequest.resolve({ revision: revision + 1, items: [topic("A-obsolete")], snapshotId: "removed-project-read", complete: true }));
  await flush();
  assert.ok(labels().includes("A-returned"));
  assert.ok(!labels().includes("A-obsolete"), "removed/re-added project cannot reuse an old request generation");
  assert.ok(releasedSnapshots.includes("removed-project-read"));
  await unmount();
  console.log("  PASS  project removal/re-addition fences late responses and releases obsolete snapshots");

  let projectSyncs = 0;
  await mount(false, false, async () => { projectSyncs++; });
  const firstProject = container.querySelector<HTMLElement>(".project-tree__folder--project");
  assert.ok(firstProject, "project folder exists");
  await act(async () => firstProject.dispatchEvent(new dom.window.MouseEvent("contextmenu", { bubbles: true, clientX: 30, clientY: 30 })));
  await flush();
  const removeItem = () => [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    .find(button => ["Remove", "Confirm remove"].includes(button.textContent?.trim() ?? ""));
  assert.ok(removeItem(), "remove project command exists");
  await act(async () => removeItem()!.click()); await flush();
  assert.ok(removeItem(), "remove project command asks for confirmation");
  await act(async () => removeItem()!.click()); await flush();
  assert.deepEqual(removedWorkspaces, [roots[0]], "the selected workspace is removed");
  assert.equal(projectSyncs, 1, "workspace removal synchronizes tabs and the active session");
  await unmount();
  console.log("  PASS  project removal synchronizes the application session surface");

  await mount();
  snapshotFailure = new Error("workspace snapshot failed");
  const addMenus = container.querySelectorAll<HTMLButtonElement>('.project-tree__header-menu-wrap button[aria-haspopup="menu"]');
  assert.ok(addMenus.length > 0, "add project menu exists");
  await act(async () => addMenus[addMenus.length - 1].click()); await flush();
  const addFolder = document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')[1];
  assert.ok(addFolder, "add local folder command exists");
  await act(async () => addFolder.click()); await flush();
  assert.ok(document.querySelector(".toast--error")?.textContent?.includes("workspace snapshot failed"), "add project reports snapshot refresh failure");
  await unmount();
  console.log("  PASS  add project reports shell refresh failures");
} finally {
  mock.timers.reset();
  dom.window.close();
}
