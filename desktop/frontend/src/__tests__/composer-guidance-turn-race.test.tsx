import assert from "node:assert/strict";
import { act } from "react";
import { installDom, installBridgeApp, renderComposer } from "./composerInboxHarness";

const dom = installDom();
const target = { tabId: "tab-a", sessionPath: "session-a", generation: 1, selection: 0, remote: false };
let resolveReceipt!: (value: { itemId: string; disposition: string; position: number; paused: boolean }) => void;
const receipt = new Promise<{ itemId: string; disposition: string; position: number; paused: boolean }>(resolve => { resolveReceipt = resolve; });
let submitted: unknown[] | undefined;
let saved = false;
installBridgeApp({
  CaptureInboxTarget: async () => target,
  ListTabs: async () => [{ id: "tab-a" }],
  InboxSnapshot: async () => ({ sessionPath: "session-a", revision: saved ? 2 : 1, mutationsSupported: true,
    items: saved ? [{ id: "saved-guidance", preview: "preserve my guidance", state: "queued", intent: "followup", source: "desktop" }] : [] }),
  EnqueueInboxFollowupForTarget: async (...args: unknown[]) => { submitted = args; return receipt; },
  InboxQueueForTarget: async () => { throw new Error("missing turn must queue as follow-up"); },
});
const { root, rerender } = await renderComposer({ running: true, localDurableGuidance: false,
  onSteer: async () => { throw new Error("captured targets must use durable receipt tracking"); } });
await rerender({ insertRequest: { id: 9101, text: "preserve my guidance", mode: "replace" } });
const button = document.querySelector<HTMLButtonElement>(".composer__queue-steer");
assert.ok(button);
await act(async () => { button.click(); });
for (let i = 0; i < 50 && !submitted; i++) {
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 0)); });
}
assert.ok(submitted, "a missing turn must reach durable follow-up submission");
assert.deepEqual(submitted.slice(0, 4), [target, "preserve my guidance", "preserve my guidance", []]);
assert.match(String(submitted[4]), /^followup-/);
assert.equal(document.querySelector<HTMLTextAreaElement>("textarea.composer__input")?.value, "preserve my guidance", "draft must survive until durable receipt");
await act(async () => {
  saved = true;
  resolveReceipt({ itemId: "saved-guidance", disposition: "queued_followup", position: 1, paused: false });
  await new Promise(resolve => setTimeout(resolve, 0));
});
const renderDeadline = Date.now() + 5000;
while (Date.now() < renderDeadline && !document.querySelector(".inbox-queue__list")) {
  await act(async () => { await new Promise(resolve => setTimeout(resolve, 10)); });
}
assert.ok(document.querySelector(".inbox-queue__list")?.textContent?.includes("preserve my guidance"), `durable receipt should show queued guidance: ${document.body.textContent}`);
assert.equal(document.querySelector<HTMLTextAreaElement>("textarea.composer__input")?.value, "", "only the receipt clears the draft");
assert.equal(document.querySelector(".toast--warn"), null);
await act(async () => root.unmount());
dom.window.close();
console.log("composer turn-discovery race preserves guidance through durable receipt");
