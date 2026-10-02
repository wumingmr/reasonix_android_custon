import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import type { SessionMeta } from "../lib/types";

const dom = new JSDOM('<html><body><div id="root"></div></body></html>', { url: "http://localhost/", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, Node: dom.window.Node,
  Element: dom.window.Element, HTMLElement: dom.window.HTMLElement,
  Event: dom.window.Event, MouseEvent: dom.window.MouseEvent,
  IS_REACT_ACT_ENVIRONMENT: true,
});
Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true });
const { createRoot } = await import("react-dom/client");
const { HistoryPanel } = await import("../components/HistoryPanel");
const { LocaleProvider } = await import("../lib/i18n");
const root = createRoot(document.getElementById("root")!);
const renamed: string[] = [];
const session: SessionMeta = {
  path: "/sessions/one.jsonl", title: "Old name", preview: "Old name",
  turns: 1, createdAt: Date.now(), lastActivityAt: Date.now(), modTime: Date.now(),
  current: false, open: false,
};

try {
  await act(async () => root.render(<LocaleProvider><HistoryPanel
    sessions={[session]} running={false} onResume={() => {}} onPreview={async () => []}
    onDelete={() => {}} onRename={(_session, title) => renamed.push(title)} onClose={() => {}}
  /></LocaleProvider>));
  const row = document.querySelector(".hist-item");
  assert.ok(row);
  await act(async () => row.dispatchEvent(new dom.window.MouseEvent("contextmenu", { bubbles: true, clientX: 30, clientY: 30 })));
  const rename = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find(button => button.textContent?.includes("Rename"));
  assert.ok(rename, "history menu offers rename");
  await act(async () => rename.click());
  const input = document.querySelector<HTMLInputElement>(".hist-item__rename");
  assert.ok(input, "history rename opens an input");
  const composingEnter = new dom.window.KeyboardEvent("keydown", { key: "Enter", isComposing: true, bubbles: true });
  const safariEnter = new dom.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true });
  Object.defineProperty(safariEnter, "keyCode", { value: 229 });
  await act(async () => { input.dispatchEvent(composingEnter); input.dispatchEvent(safariEnter); });
  assert.equal(document.querySelector(".hist-item__rename"), input, "IME Enter keeps history rename open");
  assert.deepEqual(renamed, [], "IME Enter does not persist an unfinished name");
  await act(async () => input.dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  assert.deepEqual(renamed, ["Old name"], "ordinary Enter commits the name once");
  assert.equal(document.querySelector(".hist-item__rename"), null);
  console.log("history session rename: IME and ordinary Enter passed");
} finally {
  await act(async () => root.unmount());
  dom.window.close();
}
