import { JSDOM } from "jsdom";
import { registerHooks } from "node:module";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { WorkspacePanel } from "../components/WorkspacePanel";
import type { AppBindings } from "../lib/bridge";
import { LocaleProvider } from "../lib/i18n";
import type { GitCommitView, WireCompletionSummary, WorkspaceChangeDetailView, WorkspaceChangesView } from "../lib/types";
import { resetWorkspaceTreeMemoryForTests } from "../lib/workspaceTreeMemory";
import { setFileNavigationOwner } from "../lib/fileNavigationCommands";
import { useActivityBarStore } from "../store/activityBar";
import { installDesktopHostStub } from "./desktopHostStub";

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.endsWith(".css")) {
      return nextResolve("./asset-stub-for-tests.ts", { ...context, parentURL: import.meta.url });
    }
    return nextResolve(specifier, context);
  },
});

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

export function flushPromises(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// Text input events dispatched in this jsdom do not reach React's onChange, so
// a suite that must drive a controlled field calls the mounted element's own
// `onChange` through its `__reactProps$` entry instead (see
// `ask-card-layout.test.ts` and `remote-file-navigation-races.test.tsx`).

export async function waitFor(label: string, predicate: () => boolean) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    await act(async () => {
      await flushPromises();
    });
    if (predicate()) return;
  }
  throw new Error(`timed out waiting for ${label}`);
}

function installDom() {
  const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", {
    pretendToBeVisual: true,
    url: "http://localhost/",
  });
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.window = dom.window as unknown as Window & typeof globalThis;
  globalThis.document = dom.window.document;
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  globalThis.Node = dom.window.Node;
  globalThis.Element = dom.window.Element;
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.Event = dom.window.Event;
  globalThis.CustomEvent = dom.window.CustomEvent;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.PointerEvent = dom.window.MouseEvent as unknown as typeof PointerEvent;
  globalThis.MutationObserver = dom.window.MutationObserver;
  globalThis.ResizeObserver = TestResizeObserver;
  dom.window.ResizeObserver = TestResizeObserver;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  (dom.window.HTMLElement.prototype as unknown as { attachEvent: () => void }).attachEvent = () => {};
  (dom.window.HTMLElement.prototype as unknown as { detachEvent: () => void }).detachEvent = () => {};
  Object.defineProperty(dom.window.HTMLElement.prototype, "scrollIntoView", { configurable: true, value: () => {} });
  Object.defineProperty(dom.window.HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 320,
  });
  Object.defineProperty(dom.window.HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get: function offsetHeight(this: HTMLElement) {
      return this.classList.contains("workspace-tree") ? 300 : this.dataset.index ? 24 : 0;
    },
  });
  Object.defineProperty(dom.window.HTMLElement.prototype, "getBoundingClientRect", {
    configurable: true,
    value: function getBoundingClientRect(this: HTMLElement) {
      const width = 320;
      const height = this.classList.contains("workspace-tree") ? 300 : this.dataset.index ? 24 : 0;
      return {
        x: 0,
        y: 0,
        top: 0,
        left: 0,
        right: width,
        bottom: height,
        width,
        height,
        toJSON: () => ({}),
      } as DOMRect;
    },
  });
  return dom;
}

export async function renderWorkspace(
  changes: WorkspaceChangesView,
  options: { history?: GitCommitView[]; detail?: WorkspaceChangeDetailView; completionSummary?: WireCompletionSummary } = {},
) {
  resetWorkspaceTreeMemoryForTests();
  setFileNavigationOwner(null);
  const dom = installDom();
  await act(async () => {
    useActivityBarStore.setState({ workspaceRoot: "/repo", tabs: [], activeTabId: null });
  });
  const dockTabId = useActivityBarStore.getState().openEntry("file", "Files");
  const desktopStub = installDesktopHostStub(({
    main: {
      App: {
        ListDirForTab: async () => [],
        WorkspaceGitHistory: async () => options.history ?? [],
        WorkspaceChanges: async () => changes,
        WorkspaceChangeDetail: async () => options.detail ?? {},
        ResolveWorkspacePathForTab: async (_tabID, path) => path.startsWith("/") ? path : `/repo/${path}`,
        ReadFileForTab: async (_tabID, path) => ({ path, body: "", size: 0, truncated: false, binary: false }),
      } as Partial<AppBindings> as AppBindings,
    },
  }).main.App);
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  await act(async () => {
    root.render(
      <LocaleProvider>
        <WorkspacePanel
          open
          tabId="tab-a"
          dockTabId={dockTabId}
          cwd="/repo"
          maximized={false}
          initialViewMode="changed"
          completionSummary={options.completionSummary}
          onClose={() => {}}
          onToggleMaximized={() => {}}
        />
      </LocaleProvider>,
    );
    await flushPromises();
  });
  await waitFor("workspace changes", () => Boolean(document.querySelector(".workspace-preview__body")));
  return { dom, root, dockTabId };
}

export async function renderFilesWorkspace(methods: Partial<AppBindings>, props: Partial<Parameters<typeof WorkspacePanel>[0]> = {}) {
  resetWorkspaceTreeMemoryForTests();
  // Each mount gets its own navigation instance, and the file dock tab a
  // command targets is the one this panel renders — exactly as the region does.
  setFileNavigationOwner(null);
  const dom = installDom();
  await act(async () => {
    useActivityBarStore.setState({ workspaceRoot: "/repo", tabs: [], activeTabId: null });
  });
  const dockTabId = useActivityBarStore.getState().openEntry("file", "Files");
  const desktopStub = installDesktopHostStub(({
    main: {
      App: {
        ListDirForTab: async () => [],
        SearchFileRefsForTab: async () => [],
        WorkspaceGitHistory: async () => [],
        WorkspaceChanges: async () => ({ files: [], gitAvailable: true }),
        WorkspaceChangeDetail: async () => ({}),
        ResolveWorkspacePathForTab: async (_tabID, path) => path.startsWith("/") ? path : `/repo/${path}`,
        ResolvePresentedPathForTab: async (_tabID, _toolCallID, path) => path.startsWith("/") ? path : `/repo/${path}`,
        ReadFileForTab: async (_tabID, path) => ({ path, body: "", size: 0, truncated: false, binary: false }),
        ...methods,
      } as Partial<AppBindings> as AppBindings,
    },
  }).main.App);
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  let currentProps: Parameters<typeof WorkspacePanel>[0] = {
    open: true,
    tabId: "tab-a",
    cwd: "/repo",
    maximized: false,
    initialViewMode: "files",
    onClose: () => {},
    onToggleMaximized: () => {},
    dockTabId,
    ...props,
  };
  const rerender = async (nextProps: Partial<Parameters<typeof WorkspacePanel>[0]> = {}) => {
    currentProps = { ...currentProps, ...nextProps };
    await act(async () => {
      root.render(
        <LocaleProvider>
          <WorkspacePanel {...currentProps} />
        </LocaleProvider>,
      );
      await flushPromises();
    });
  };
  await rerender();
  return { dom, root, rerender, dockTabId };
}
