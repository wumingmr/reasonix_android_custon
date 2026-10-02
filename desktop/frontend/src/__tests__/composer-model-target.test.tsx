import assert from "node:assert/strict";
import { act } from "react";
import { installDom, installBridgeApp, renderComposer } from "./composerInboxHarness";
import { emptySessionInput } from "../lib/sessionComposerPersistence";
import { makeSessionUIMock } from "../lib/sessionUIMock";

const dom = installDom();
globalThis.HTMLInputElement = dom.window.HTMLInputElement;
const calls: string[] = [];
const picked: string[] = [];
const catalog = [
  { ref: "configured/flash", provider: "configured", model: "flash", current: true },
  { ref: "configured/pro", provider: "configured", model: "pro", current: false },
];
installBridgeApp({
  ...makeSessionUIMock(async () => {}),
  ModelsForTab: async (id: string) => { calls.push(`tab:${id}`); return catalog; },
  ModelsForDraft: async (id: string) => {
    calls.push(`draft:${id}`);
    return id === "legacy-draft" ? catalog : [];
  },
});
async function waitFor(check: () => boolean) {
  for (let i = 0; i < 100 && !check(); i++) {
    await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)); });
  }
  assert.ok(check(), "model picker should become ready");
}
const { root, rerender } = await renderComposer({
  tabId: "formal-tab", sessionKey: "formal-session", modelLabel: "flash",
  formalSessionRef: { hostId: "local", sessionId: "formal-session" },
  composerTarget: { kind: "session", tabId: "formal-tab" },
  onSwitchModel: async ref => { picked.push(ref); return true; },
});
try {
  await waitFor(() => calls.length > 0);
  assert.ok(calls.every(call => call === "tab:formal-tab"), `formal input persistence must not select the draft catalog: ${calls}`);
  await waitFor(() => !!document.querySelector<HTMLButtonElement>(".modelsw__trigger:not(:disabled)"));
  await act(async () => { document.querySelector<HTMLButtonElement>(".modelsw__trigger")!.click(); });
  await waitFor(() => document.querySelectorAll("[role=option]").length === 2);
  await act(async () => { document.querySelector<HTMLButtonElement>("[role=option][aria-selected=false]")!.click(); });
  assert.deepEqual(picked, ["configured/pro"]);

  calls.length = 0;
  await rerender({ tabId: "other-tab", sessionKey: "other-session",
    formalSessionRef: { hostId: "local", sessionId: "other-session" },
    composerTarget: { kind: "session", tabId: "other-tab" } });
  await waitFor(() => calls.length > 0);
  assert.ok(calls.every(call => call === "tab:other-tab"), "navigation must query the new formal tab");

  calls.length = 0;
  await rerender({ tabId: undefined, formalSessionRef: undefined, sessionKey: "legacy-draft",
    composerTarget: { kind: "draft", draftId: "legacy-draft", generation: 1 },
    persistentDraft: { draftId: "legacy-draft", generation: 1, revision: 1, initial: emptySessionInput(), onChange() {} } });
  await waitFor(() => calls.length > 0);
  assert.ok(calls.every(call => call === "draft:legacy-draft"), "recovered legacy drafts retain their catalog route");
  await act(async () => { document.querySelector<HTMLButtonElement>(".modelsw__trigger")!.click(); });
  await waitFor(() => document.querySelectorAll("[role=option]").length === 2);
  console.log("PASS composer model catalog: formal persisted input, switching, navigation and recovered legacy draft");
} finally {
  await act(async () => root.unmount());
  dom.window.close();
}
