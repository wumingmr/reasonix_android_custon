import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { Fragment, act } from "react";
import { createRoot } from "react-dom/client";
import type { AppBindings } from "../lib/bridge";
import { installDesktopHostStub } from "./desktopHostStub";
import { parseMarkdownToBlocks } from "../lib/markdownPipeline";
import { hastBlockToJsx } from "../lib/hastJsx";
import { createComponents } from "../components/markdownComponents";
import { LocaleProvider } from "../lib/i18n";
import { useBrowserPanelStore } from "../lib/browserPanelStore";

const dom = new JSDOM("<!doctype html><html><body><div id=\"root\"></div></body></html>", { url: "http://localhost/" });
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, Node: dom.window.Node,
  HTMLElement: dom.window.HTMLElement, MouseEvent: dom.window.MouseEvent,
  Event: dom.window.Event, IS_REACT_ACT_ENVIRONMENT: true,
});

let resolveReferences!: (value: {
  turnKey: string;
  references: Array<{ key: string; path: string; status: "resolved"; displayPath: string; kind: string; actions: string[] }>;
}) => void;
let pendingCandidates: Array<{ key: string; path: string }> = [];
let nativeOpens = 0;
const previewCalls: string[] = [];
const browserTab = {
  id: "html-tab", taskId: "tab-a", url: "http://127.0.0.1/preview", title: "index.html",
  loading: false, canGoBack: false, canGoForward: false, temporary: false,
  mode: "agent" as const, epoch: 0, zoom: 1, error: null,
};

const stub = installDesktopHostStub(({
  main: { App: {
    ResolveChatFileReferencesForTab: async (_tabId: string, turnKey: string, candidates: Array<{ key: string; path: string }>) => {
      pendingCandidates = candidates;
      return new Promise((resolve) => { resolveReferences = resolve; });
    },
    OpenFileBrowserPreviewForTab: async (_tabId: string, request: { path: string }) => {
      previewCalls.push(request.path);
      return { tabId: browserTab.id, url: browserTab.url, status: "opened", sessionGeneration: 1 };
    },
    OpenLocalPath: async () => { nativeOpens += 1; },
  } as Partial<AppBindings> as AppBindings },
}).main.App);

useBrowserPanelStore.setState({
  host: {
    list: async () => [browserTab], activate: async () => {}, close: async () => {},
  } as unknown as NonNullable<ReturnType<typeof useBrowserPanelStore.getState>["host"]>,
  taskId: "tab-a", tabs: [], shown: [], visible: true,
});

const blocks = parseMarkdownToBlocks("[Open the page](file:///repo/out/index.html)");
const components = createComponents(false);
const { ChatFileScopeProvider, ChatFileTurnProvider, useChatFileCandidateReport } = await import("../components/ChatFileLinkContext");
function Reporter() {
  useChatFileCandidateReport(blocks, 1);
  return null;
}

const rootElement = document.getElementById("root")!;
const root = createRoot(rootElement);
await act(async () => {
  root.render(<LocaleProvider>
    <ChatFileScopeProvider scopeKey="session-a" tabId="tab-a">
      <ChatFileTurnProvider turnKey="turn-a" factsVersion={1} presentedFiles={[]} modifiedFiles={[]} tabId="tab-a">
        <Reporter />
        {blocks.map((block) => <Fragment key={block.key}>{hastBlockToJsx(block, components)}</Fragment>)}
      </ChatFileTurnProvider>
    </ChatFileScopeProvider>
  </LocaleProvider>);
});
await act(async () => { await Promise.resolve(); await Promise.resolve(); });

const pendingLink = rootElement.querySelector<HTMLAnchorElement>("a");
assert.ok(pendingLink, "the explicit local file remains a pending in-app link");
await act(async () => {
  pendingLink.dispatchEvent(new dom.window.MouseEvent("click", { bubbles: true, cancelable: true }));
});
assert.equal(nativeOpens, 0, "the first click never falls through to the OS opener");
assert.equal(previewCalls.length, 0, "opening waits for host verification");

await act(async () => {
  resolveReferences({
    turnKey: "turn-a",
    references: pendingCandidates.map((candidate) => ({
      key: candidate.key, path: candidate.path, status: "resolved", displayPath: "out/index.html",
      kind: "html", actions: ["preview", "source", "browser", "reveal-tree", "copy-path", "save-copy"],
    })),
  });
  await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
});

assert.deepEqual(previewCalls, ["out/index.html"], "the queued first click opens the host-verified HTML in the built-in browser");
assert.equal(nativeOpens, 0);
assert.equal(useBrowserPanelStore.getState().activeTabId, browserTab.id);

await act(async () => { root.unmount(); });
stub.uninstall();
dom.window.close();
console.log("PASS unresolved local HTML clicks wait for verification and stay in-app");
