import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { Composer } from "../components/Composer";
import { LocaleProvider } from "../lib/i18n";
import { ToastProvider } from "../lib/toast";
import { flushAllSessionComposers, resumeSessionComposerEditing } from "../lib/sessionComposerPersistence";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { installDesktopHostStub } from "./desktopHostStub";
import { createFileUploadMock, dispatchPasteFile } from "./fileUploadStub";

function deferred<T = void>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}
const tick = () => new Promise<void>(done => setImmediate(done));
const textarea = () => document.querySelector<HTMLTextAreaElement>("textarea")!;
const sendButton = () => document.querySelector<HTMLButtonElement>(".composer__btn--send")!;
async function menu(index: number) {
  await act(async () => {
    textarea().dispatchEvent(new window.MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }));
  });
  await act(async () => { document.querySelectorAll<HTMLButtonElement>(".context-menu__item")[index].click(); await tick(); });
}
async function fixture(session: string, commands: Record<string, unknown> = {}, formal = true) {
  const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost/", pretendToBeVisual: true });
  Object.assign(globalThis, {
    window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage,
    IS_REACT_ACT_ENVIRONMENT: true,
    requestAnimationFrame: dom.window.requestAnimationFrame.bind(dom.window),
    cancelAnimationFrame: dom.window.cancelAnimationFrame.bind(dom.window),
    ResizeObserver: class { observe() {} unobserve() {} disconnect() {} },
  });
  for (const name of ["Node", "HTMLElement", "HTMLTextAreaElement", "Event", "CustomEvent", "File", "FileReader", "MutationObserver"]) {
    Object.defineProperty(globalThis, name, { configurable: true, value: Reflect.get(dom.window, name) });
  }
  Object.defineProperty(globalThis, "navigator", { configurable: true, value: dom.window.navigator });
  Object.defineProperty(dom.window.HTMLElement.prototype, "attachEvent", { configurable: true, value() {} });
  Object.defineProperty(dom.window.HTMLElement.prototype, "detachEvent", { configurable: true, value() {} });
  window.matchMedia = (() => ({ matches: true, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
  const backend = makeSessionUIMock(async () => {});
  const host = installDesktopHostStub({
    Commands: async () => [], Models: async () => [], ModelsForTab: async () => [],
    ListDir: async () => [], ListDirForTab: async () => [], SearchFileRefs: async () => [], SearchFileRefsForTab: async () => [],
    ...backend, ...commands,
  }, { clipboardWriteResult: () => false });
  const ref = { hostId: "local", sessionId: session };
  const sent: string[] = [];
  let props: Parameters<typeof Composer>[0] = {
    running: false, collaborationMode: "normal", toolApprovalMode: "ask", goal: "", cwd: "/repo",
    modelLabel: "DeepSeek-R1", tabId: session, sessionKey: session, ready: true,
    formalSessionRef: formal ? ref : undefined,
    onSend: async text => { sent.push(text); },
    onCancel: async () => ({ discardedItemIds: [] }), onCycleMode() {}, onSetMode() {},
    onSetCollaborationMode() {}, onSetToolApprovalMode() {}, onClearGoal() {}, onSwitchModel() {}, onSetEffort() {},
  };
  const root = createRoot(document.getElementById("root")!);
  const paint = async (next: Partial<typeof props> = {}) => {
    props = { ...props, ...next };
    await act(async () => { root.render(<LocaleProvider><ToastProvider><Composer {...props}/></ToastProvider></LocaleProvider>); await tick(); });
  };
  await paint();
  let insertId = 0;
  return { ref, backend, sent, paint,
    async insert(text: string) { await paint({ insertRequest: { id: ++insertId, text, mode: "replace" } }); },
    async select(from: number, to = from) {
      await act(async () => { await new Promise<void>(done => requestAnimationFrame(() => done())); });
      textarea().setSelectionRange(from, to);
    },
    saved: async () => JSON.parse((await backend.GetSessionComposerState(ref)).contentJson),
    async dispose() {
      await act(async () => { resumeSessionComposerEditing(); root.unmount(); });
      host.uninstall(); dom.window.close();
    },
  };
}

for (const kind of ["file", "image", "clipboard", "native"] as const) {
  test(`accepted ${kind} input blocks sending through publication and close`, { timeout: 15000 }, async () => {
    const entered = deferred(), release = deferred();
    const f = await fixture(kind, {
      CaptureAttachmentTarget: async () => { entered.resolve(); await release.promise; return { token: "input-target", capabilities: ["attachments-v2"] }; },
      ReleaseAttachmentTarget: async () => {},
      StageImageForTarget: async () => ({ draftId: "0123456789abcdef0123456789abcdef", displayName: "accepted.png", mime: "image/png", width: 1, height: 1, bytes: 12 }),
      SavePastedImageForComposerTarget: async () => ".reasonix/attachments/accepted.png",
      SaveClipboardImageForTarget: async () => ".reasonix/attachments/native.png",
      AttachmentDataURLForTarget: async () => "data:image/png;base64,aGVsbG8=",
      ReadDraftImageForTarget: async () => "data:image/png;base64,aGVsbG8=",
    });
    const restoreUpload = createFileUploadMock("input-target", {
      ok: (value, message) => assert.ok(value, message), eq: (a, b, message) => assert.equal(a, b, message),
    })(async () => ".reasonix/attachments/accepted.txt");
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
      read: async () => kind === "native" ? [{ types: ["image/png"] }] : [],
      readText: async () => { entered.resolve(); await release.promise; return " ACCEPTED PASTE"; },
    }});
    try {
      await f.insert("original message");
      await f.select(16);
      if (kind === "clipboard" || kind === "native") await menu(4);
      else await act(async () => { dispatchPasteFile(textarea(), new File(["hello"], kind === "image" ? "accepted.png" : "accepted.txt", { type: kind === "image" ? "image/png" : "text/plain" })); });
      await entered.promise;
      assert.equal(sendButton().disabled, true, "send is blocked even before target capture/read completes");
      await act(async () => {
        sendButton().click();
        textarea().dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
        await tick();
      });
      assert.deepEqual(f.sent, []);
      let closed = false, closing!: Promise<void>;
      await act(async () => { closing = flushAllSessionComposers().then(() => { closed = true; }); await tick(); });
      assert.equal(closed, false, "close waits for accepted input");
      await act(async () => { release.resolve(); await closing; });
      const saved = await f.saved();
      if (kind === "clipboard") assert.equal(saved.text, "original message ACCEPTED PASTE");
      else { assert.equal(saved.text, "original message"); assert.equal(saved.attachments.length, 1); }
      await act(async () => resumeSessionComposerEditing());
      assert.equal(sendButton().disabled, false, "completed input releases the send guard");
      if (kind === "clipboard") {
        await act(async () => { sendButton().click(); await tick(); });
        await act(async () => { await flushAllSessionComposers(); });
        assert.deepEqual(f.sent, ["original message ACCEPTED PASTE"]);
        assert.deepEqual(await f.saved(), {}, "only the complete submitted draft is cleared");
      }
    } finally { release.resolve(); restoreUpload(); await f.dispose(); }
  });
}

for (const kind of ["changed", "unchanged", "background", "local-changed", "clipboard-failed"] as const) {
  test(`cut preserves ${kind} content and participates in close draining`, { timeout: 15000 }, async () => {
    const entered = deferred(), release = deferred();
    const f = await fixture("cut-" + kind, {}, kind !== "local-changed");
    let copied = "";
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
      writeText: async (text: string) => {
        entered.resolve(); await release.promise;
        if (kind === "clipboard-failed") throw Error("clipboard unavailable");
        copied = text;
      },
    }});
    try {
      await f.insert("abcdef");
      await f.select(1, 4);
      await menu(2);
      await entered.promise;
      if (kind === "changed" || kind === "local-changed") await f.insert("NEW CONTENT");
      if (kind === "background") await f.paint({
        tabId: "other", sessionKey: "other", formalSessionRef: { hostId: "local", sessionId: "other" }, insertRequest: null,
      });
      let closed = false, closing!: Promise<void>;
      if (kind !== "local-changed") {
        await act(async () => { closing = flushAllSessionComposers().then(() => { closed = true; }); await tick(); });
        assert.equal(closed, false, "close waits for an admitted cut");
      }
      await act(async () => { release.resolve(); if (closing) await closing; else await tick(); });
      assert.equal(copied, kind === "clipboard-failed" ? "" : "bcd");
      if (kind === "local-changed") assert.equal(textarea().value, "NEW CONTENT");
      else {
        const expected = kind === "changed" ? "NEW CONTENT" : kind === "clipboard-failed" ? "abcdef" : "aef";
        assert.equal((await f.saved()).text, expected);
        await act(async () => resumeSessionComposerEditing());
        if (kind === "background") {
          assert.equal(textarea().value, "", "background cut never mutates the current composer");
          await f.paint({ tabId: f.ref.sessionId, sessionKey: f.ref.sessionId, formalSessionRef: f.ref });
        }
        assert.equal(textarea().value, expected);
      }
    } finally { release.resolve(); await f.dispose(); }
  });
}

test("native paste fallback owns input before its timer fires", { timeout: 15000 }, async () => {
  const entered = deferred(), release = deferred();
  const f = await fixture("native-timer", {
    CaptureAttachmentTarget: async () => { entered.resolve(); await release.promise; return { token: "timer-target", capabilities: ["attachments-v2"] }; },
    ReleaseAttachmentTarget: async () => {},
    SaveClipboardImageForTarget: async () => ".reasonix/attachments/native.png",
    AttachmentDataURLForTarget: async () => "data:image/png;base64,aGVsbG8=",
  });
  const originalTimer = window.setTimeout;
  let fire!: () => void;
  try {
    await f.insert("with native image");
    window.setTimeout = ((handler: TimerHandler, delay?: number, ...args: unknown[]) => {
      if (delay === 160) { fire = handler as () => void; return -1; }
      return originalTimer(handler, delay, ...args);
    }) as typeof window.setTimeout;
    await act(async () => { textarea().dispatchEvent(new window.KeyboardEvent("keydown", { key: "v", ctrlKey: true, bubbles: true })); });
    assert.equal(typeof fire, "function");
    assert.equal(sendButton().disabled, true);
    let closed = false, closing!: Promise<void>;
    await act(async () => { closing = flushAllSessionComposers().then(() => { closed = true; }); await tick(); });
    assert.equal(closed, false);
    await act(async () => { fire(); await entered.promise; });
    assert.equal(closed, false);
    await act(async () => { release.resolve(); await closing; });
    const saved = await f.saved();
    assert.equal(saved.text, "with native image");
    assert.equal(saved.attachments[0].path, ".reasonix/attachments/native.png");
  } finally { window.setTimeout = originalTimer; release.resolve(); await f.dispose(); }
});
