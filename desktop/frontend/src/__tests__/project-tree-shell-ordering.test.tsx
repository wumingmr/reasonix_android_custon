// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/project-tree-shell-ordering.test.tsx
import assert from "node:assert/strict";
import { mock } from "node:test";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import type { ReasonixDesktopHost } from "../lib/desktopHost";
import type { ProjectNode, ProjectTreeSnapshot } from "../lib/types";

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

const { createRoot } = await import("react-dom/client");
const { ProjectTree } = await import("../components/ProjectTree");
const { LocaleProvider } = await import("../lib/i18n");
const { ToastProvider } = await import("../lib/toast");

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

const pending: ReturnType<typeof deferred<ProjectTreeSnapshot>>[] = [];
const catalog = { state: "ready" as const, revision: 7, indexed: 0, total: 0, repairPending: 0 };
const bindings = {
  GetProjectTreeSnapshot: () => {
    const request = deferred<ProjectTreeSnapshot>();
    pending.push(request);
    return request.promise;
  },
  ListProjectTopics: async () => ({ revision: 7, items: [], complete: true }),
  GetSessionCatalogStatus: async () => catalog,
  GetSessionOrganization: async () => ({ groups: [], revision: 7, order: [], manualOrderEnabled: false }),
  GetProjectTreeRuntimeSnapshot: async () => ({ revision: 0, topics: [] }),
  IsolatedWorktreeAvailability: async () => ({ available: true, reason: "" }),
  Platform: async () => "darwin",
  RemoteConnectionStatuses: async () => [],
};
window.reasonixDesktop = {
  kind: "electron", contract: { commands: Object.keys(bindings) }, platform: { os: "darwin" }, native: {},
  invoke: (method: string, args: unknown[]) => (bindings as unknown as Record<string, (...args: unknown[]) => Promise<unknown>>)[method](...args),
  on: () => () => {},
} as unknown as ReasonixDesktopHost;

const project = (label: string): ProjectNode => ({ key: `project-${label}`, kind: "project", label, root: `/ordering-${label}`, children: [] });
const a = project("A"), b = project("B");
const snapshot = (projects: ProjectNode[]): ProjectTreeSnapshot => ({ revision: 7, projects, catalog });
const container = document.getElementById("root")!;
const flush = async () => { await act(async () => { await new Promise<void>(resolve => setImmediate(resolve)); }); };
const labels = () => [...container.querySelectorAll(".project-tree__folder--project .project-tree__folder-main")].map(el => el.textContent?.trim());
const noop = () => {};
const addProject = async () => {};

mock.timers.enable({ apis: ["setTimeout", "Date"] });
try {
  for (const scenario of [
    { name: "addition", before: [a], after: [a, b] },
    { name: "removal", before: [a, b], after: [a] },
  ]) {
    pending.length = 0;
    localStorage.clear();
    const root = createRoot(container);
    const render = async (refreshSignal: number) => {
      await act(async () => root.render(<LocaleProvider><ToastProvider><ProjectTree
        onOpenTopic={noop} onAddProject={addProject} refreshSignal={refreshSignal}
      /></ToastProvider></LocaleProvider>));
      await flush();
    };
    try {
      await render(0);
      assert.equal(pending.length, 1, "mount starts one shell read");
      assert.match(container.querySelector(".project-tree__empty-state")?.textContent ?? "", /读取|讀取|Reading/,
        "an unanswered shell read must say it is loading");
      assert.doesNotMatch(container.textContent ?? "", /No projects yet|还没有项目|還沒有專案/,
        "pending shell read must not claim there are no projects");
      if (scenario.name === "addition") {
        assert.equal(container.querySelector(".project-tree__empty-state .project-tree__skeleton"), null,
          "fast shell reads must not flash a skeleton");
        await act(async () => mock.timers.tick(250));
        assert.ok(container.querySelector(".project-tree__empty-state .project-tree__skeleton"),
          "a delayed shell read shows a skeleton");
      }
      await act(async () => pending[0].resolve(snapshot(scenario.before)));
      await flush();
      assert.deepEqual(labels(), scenario.before.map(item => item.label));
      assert.equal(container.querySelector(".project-tree__empty-state .project-tree__skeleton"), null);

      // A read started before the mutation retains the old shell contents.
      // Its scalar revision may equal the post-mutation read: this fixture
      // exercises client ordering, not proof of a particular backend cause.
      await render(1);
      assert.equal(pending.length, 2);
      await render(2);
      assert.equal(pending.length, 3);
      await act(async () => pending[2].resolve(snapshot(scenario.after)));
      await flush();
      assert.deepEqual(labels(), scenario.after.map(item => item.label), "latest state is initially painted");

      await act(async () => pending[1].resolve(snapshot(scenario.before)));
      await flush();
      assert.deepEqual(labels(), scenario.after.map(item => item.label),
        `a delayed pre-${scenario.name} shell must not reverse the latest visible project list`);

      await render(3);
      await act(async () => pending[3].resolve({ ...snapshot(scenario.after), workspaceGeneration: 5 }));
      await flush();
      await render(4);
      await act(async () => pending[4].resolve({ ...snapshot(scenario.before), workspaceGeneration: 4, revision: 1000 }));
      await flush();
      assert.deepEqual(labels(), scenario.after.map(item => item.label), "a newer catalog cannot make older membership authoritative");
      await render(5);
      await act(async () => pending[5].resolve({ ...snapshot(scenario.before), workspaceGeneration: 6, revision: 1 }));
      await flush();
      assert.deepEqual(labels(), scenario.before.map(item => item.label), "new membership survives catalog revision reset");
      console.log(`  PASS  delayed shell response cannot reverse project ${scenario.name}`);
    } finally {
      await act(async () => root.unmount());
    }
  }

  pending.length = 0;
  localStorage.clear();
  const emptyRoot = createRoot(container);
  try {
    await act(async () => emptyRoot.render(<LocaleProvider><ToastProvider><ProjectTree
      onOpenTopic={noop} onAddProject={addProject}
    /></ToastProvider></LocaleProvider>));
    await flush();
    assert.doesNotMatch(container.textContent ?? "", /No projects yet|还没有项目|還沒有專案/);
    await act(async () => pending[0].resolve(snapshot([])));
    await flush();
    assert.match(container.textContent ?? "", /No projects yet|还没有项目|還沒有專案/,
      "an empty answer restores the true empty state");
  } finally {
    await act(async () => emptyRoot.unmount());
  }

  pending.length = 0;
  localStorage.clear();
  const failedRoot = createRoot(container);
  try {
    await act(async () => failedRoot.render(<LocaleProvider><ToastProvider><ProjectTree
      onOpenTopic={noop} onAddProject={addProject}
    /></ToastProvider></LocaleProvider>));
    await flush();
    assert.match(container.querySelector(".project-tree__empty-state")?.textContent ?? "", /读取|讀取|Reading/);
    await act(async () => pending[0].reject(new Error("snapshot unavailable")));
    await flush();
    assert.doesNotMatch(container.textContent ?? "", /读取项目|讀取專案|Reading projects/,
      "a failed first shell read must settle the loading state");
    assert.match(container.textContent ?? "", /Add new project|添加新项目|新增專案/,
      "the empty-state project action must remain available after a failed read");
    assert.match(container.textContent ?? "", /Remote connection|远程连接|遠端連線/,
      "the empty-state remote action must remain available after a failed read");
  } finally {
    await act(async () => failedRoot.unmount());
  }
} finally {
  mock.timers.reset();
  dom.window.close();
}
