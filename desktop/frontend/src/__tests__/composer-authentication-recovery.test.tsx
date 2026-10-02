// Run with the CSS stub loader and tsx.

import { JSDOM } from "jsdom";
import React from "react";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { Composer } from "../components/Composer";
import { LocaleProvider } from "../lib/i18n";
import { ToastProvider } from "../lib/toast";
import type { CollaborationMode, ToolApprovalMode } from "../lib/types";
import { installDesktopHostStub } from "./desktopHostStub";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function eq(actual: unknown, expected: unknown, label: string) {
  if (actual === expected) ok(true, label);
  else ok(false, `${label}: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

function flushTimers(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

async function waitFor(check: () => boolean, attempts = 10): Promise<void> {
  for (let i = 0; i < attempts; i++) {
    if (check()) return;
    await act(async () => {
      await flushTimers();
    });
  }
}

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
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
  globalThis.HTMLElement = dom.window.HTMLElement;
  globalThis.HTMLTextAreaElement = dom.window.HTMLTextAreaElement;
  globalThis.Event = dom.window.Event;
  globalThis.CustomEvent = dom.window.CustomEvent;
  globalThis.KeyboardEvent = dom.window.KeyboardEvent;
  globalThis.InputEvent = dom.window.InputEvent;
  globalThis.MouseEvent = dom.window.MouseEvent;
  globalThis.File = dom.window.File;
  globalThis.FileReader = dom.window.FileReader;
  globalThis.PointerEvent = dom.window.MouseEvent as unknown as typeof PointerEvent;
  globalThis.MutationObserver = dom.window.MutationObserver;
  globalThis.localStorage = dom.window.localStorage;
  globalThis.requestAnimationFrame = dom.window.requestAnimationFrame.bind(dom.window);
  globalThis.cancelAnimationFrame = dom.window.cancelAnimationFrame.bind(dom.window);
  globalThis.ResizeObserver = TestResizeObserver;
  Object.defineProperty(dom.window.HTMLElement.prototype, "attachEvent", { configurable: true, value: () => {} });
  Object.defineProperty(dom.window.HTMLElement.prototype, "detachEvent", { configurable: true, value: () => {} });
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: () => ({
      matches: true,
      media: "(prefers-reduced-motion: reduce)",
      onchange: null,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent: () => false,
    }),
  });
  return dom;
}

function installBridgeApp(methods: Record<string, unknown>) {
  installDesktopHostStub({
    Commands: async () => [],
    Models: async () => [],
    ModelsForTab: async () => [],
    ...methods,
  });
}

async function renderComposer(props: Partial<Parameters<typeof Composer>[0]> = {}) {
  const rootEl = document.getElementById("root");
  if (!rootEl) throw new Error("missing root");
  const root = createRoot(rootEl);
  let currentProps: Parameters<typeof Composer>[0] = {
    running: false,
    collaborationMode: "normal",
    toolApprovalMode: "ask" as ToolApprovalMode,

    goal: "",
    cwd: "/repo",
    modelLabel: "DeepSeek-R1",
    imageInputEnabled: true,
    tabId: "single-surface-tab",
    sessionKey: "session:project:/repo:topic-a:session-a",
    onSend: () => {},
    onCancel: async () => ({ discardedItemIds: [] }),
    onCycleMode: () => {},
    onSetMode: () => {},
    onSetCollaborationMode: (_mode: CollaborationMode) => {},
    onSetToolApprovalMode: () => {},
        onClearGoal: () => {},
    onSwitchModel: () => {},
    onSetEffort: () => {},

    ready: true,
    ...props,
  };
  const paint = async (nextProps: Partial<Parameters<typeof Composer>[0]> = {}) => {
    currentProps = { ...currentProps, ...nextProps };
    await act(async () => {
      root.render(
        <LocaleProvider>
          <ToastProvider>
            <div className="chat-pane">
              <Composer {...currentProps} />
            </div>
          </ToastProvider>
        </LocaleProvider>,
      );
      await flushTimers();
    });
  };
  await paint();
  return { root, rerender: paint };
}

function textarea(): HTMLTextAreaElement {
  const node = document.querySelector("textarea") as HTMLTextAreaElement | null;
  if (!node) throw new Error("composer textarea did not render");
  return node;
}

function sendButton(): HTMLButtonElement {
  const node = document.querySelector(".composer__btn--send") as HTMLButtonElement | null;
  if (!node) throw new Error("send button did not render");
  return node;
}


const dom = installDom();
let resolveRetry: (() => void) | undefined;
installBridgeApp({ RetryAuthenticationForTab: async () => {
  await new Promise<void>((resolve) => { resolveRetry = resolve; });
  return { status: "ready" };
} });
const rejected = { status: "authentication_rejected" as const, providerName: "relay", modelRef: "relay/chat" };
const { root, rerender } = await renderComposer({
  submitDisabled: true,
  authentication: rejected,
  insertRequest: { id: 1, text: "preserve my draft", mode: "insert" },
});
const originalDraft = textarea().value;
await waitFor(() => document.querySelectorAll(".composer-toolbar-send__configure").length === 2, 100);
const retry = Array.from(document.querySelectorAll<HTMLButtonElement>(".composer-toolbar-send__configure")).at(-1);
if (!retry) throw new Error("retry action missing");
await act(async () => { retry.click(); await flushTimers(); });
await rerender({ tabId: "other-tab", ready: false, authentication: undefined, submitDisabled: true });
await act(async () => { resolveRetry?.(); await flushTimers(); });
eq(sendButton().disabled, true, "late retry cannot enable another tab");
await rerender({ tabId: "single-surface-tab", ready: false, authentication: rejected, submitDisabled: true });
eq(sendButton().disabled, true, "retry cannot bypass controller readiness on the original tab");
await rerender({ ready: true, authentication: { status: "missing_credential", providerName: "replacement" }, submitDisabled: true });
eq(sendButton().disabled, true, "old retry cannot authorize a replacement connection");
await rerender({ authentication: { status: "ready" }, submitDisabled: false });
eq(sendButton().disabled, false, "authoritative ready state enables the preserved draft");
eq(textarea().value, originalDraft, "authentication recovery preserves draft text");
await act(async () => root.unmount());
dom.window.close();
console.log(`authentication recovery: ${passed} passed, ${failed} failed`);
if (failed) process.exitCode = 1;
