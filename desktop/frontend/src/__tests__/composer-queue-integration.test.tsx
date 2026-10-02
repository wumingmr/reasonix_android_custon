import assert from "node:assert/strict";
import { act } from "react";
import { installDom, installBridgeApp, renderComposer } from "./composerInboxHarness";
import { createInboxQueuePreviewBindings } from "../lib/inboxQueuePreview";
import type { InboxQueueRequest } from "../lib/inboxQueueCommands";
import type { InboxTarget } from "../lib/pendingFollowup";

const dom = installDom();
globalThis.sessionStorage = dom.window.sessionStorage;
const fixture = createInboxQueuePreviewBindings();
const calls: InboxQueueRequest[] = [];
let enqueues = 0;
let cancelled: string[] | undefined;
let otherHost = false;
let failSave = false;
let departOnSave = false;
installBridgeApp({
  ...fixture,
  CaptureAttachmentTarget: async () => ({ token: "queue-test-attachment", capabilities: ["attachments-v2"] }),
  ReleaseAttachmentTarget: async () => {},
  StageImageForTarget: async () => ({ draftId: "0123456789abcdef0123456789abcdef", displayName: "draft.png", mime: "image/png", width: 1, height: 1, bytes: 12 }),
  ReadDraftImageForTarget: async () => "data:image/png;base64,iVBORw0KGgo=",
  SavePastedImageForTab: async () => ".reasonix/attachments/draft.png",
  AttachmentDataURLForTab: async () => "data:image/png;base64,iVBORw0KGgo=",
  InboxSnapshot: async () => otherHost ? { revision: 1, sessionPath: "session-a", mutationsSupported: true, items: [{ id: "other-host", preview: "other host queue", state: "queued" }] } : fixture.InboxSnapshot("tab-a"),
  ListTabs: async () => [{ id: "tab-a", turnId: "live-turn" }],
  InboxQueueForTarget: async (target: InboxTarget, request: InboxQueueRequest) => {
    calls.push(request);
    if (request.kind === "edit" && failSave) return { outcome: "conflict", reason: "content_changed" };
    if (request.kind === "edit" && departOnSave) {
      await fixture.InboxQueueForTarget!(target, { kind: "delete", itemId: request.itemId });
    }
    return fixture.InboxQueueForTarget!(target, request);
  },
  EnqueueInboxFollowupForTarget: async (...args: Parameters<NonNullable<typeof fixture.EnqueueInboxFollowupForTarget>>) => {
    enqueues++;
    return fixture.EnqueueInboxFollowupForTarget!(...args);
  },
});
const flush = () => new Promise<void>(resolve => setTimeout(resolve, 20));
const view = await renderComposer({ running: true, onCancel: async ids => { cancelled = ids; return { discardedItemIds: [] }; } });
await act(async () => { await import("../components/ComposerInboxQueue"); await flush(); });
await view.rerender({ insertRequest: { id: 8001, text: "default next turn", mode: "replace" } });
await act(async () => { (document.querySelector(".composer__btn--send") as HTMLButtonElement).click(); await flush(); });
assert.equal(enqueues, 1, "ordinary send while running enqueues a follow-up");
assert.ok(!calls.some(call => call.kind === "enqueue_steer"));
await view.rerender({ insertRequest: { id: 8002, text: "explicit current turn", mode: "replace" } });
await act(async () => { (document.querySelector(".composer__queue-steer") as HTMLButtonElement).click(); await flush(); });
assert.equal(enqueues, 1);
assert.equal(calls.at(-1)?.kind, "enqueue_steer");
assert.equal(calls.at(-1)?.turnId, "live-turn");
assert.ok(calls.at(-1)?.idempotencyKey);

await view.rerender({ insertRequest: { id: 8003, text: "main draft survives editing", mode: "replace" } });
const paste = new Event("paste", { bubbles: true, cancelable: true });
Object.defineProperty(paste, "clipboardData", { value: { files: [new File(["fixture"], "draft.png", { type: "image/png" })], items: [], types: [], getData: () => "" } });
await act(async () => { document.querySelector("textarea.composer__input")!.dispatchEvent(paste); await flush(); });
const attachmentCard = document.querySelector(".composer-context__item");
assert.ok(attachmentCard, "main composer has an attachment before editing");
const edit = document.querySelector('.inbox-queue__row[data-item-id="queue-save"] button[aria-label="Edit"]') as HTMLButtonElement;
assert.ok(edit);
await act(async () => { (document.querySelector('.inbox-queue__row[data-item-id="queue-save"] button[aria-label="Edit"]') as HTMLButtonElement).click(); await flush(); });
const textarea = document.querySelector(".inbox-queue__editor textarea") as HTMLTextAreaElement;
assert.ok(textarea.value.includes("完成后验证"), "the editor receives the complete body");
assert.equal(textarea.closest("li")?.dataset.itemId, "queue-save", "editor belongs to the original row");
assert.equal(document.querySelectorAll(".inbox-queue__editor").length, 1);
assert.equal(document.querySelectorAll(".inbox-queue__row").length, 2, "editing does not expand unrelated rows");
assert.equal(textarea.closest("li")?.querySelector('.inbox-queue__body'), null, "no duplicate message preview while editing");
assert.equal(textarea.closest("li")?.querySelectorAll("footer button").length, 2, "only cancel and save in ordinary editing");
assert.equal(document.querySelector(".inbox-queue__running")?.closest(".inbox-queue__editor"), null);
assert.ok(document.querySelector(".inbox-queue__running button"), "stop stays available outside editor");
assert.equal(document.querySelector(".composer-wrap")?.getAttribute("data-queue-editing"), "true");
assert.equal(document.querySelector(".composer-context__item"), attachmentCard, "hiding preserves the mounted attachment");
await act(async () => { textarea.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true })); await flush(); });
assert.equal(calls.at(-1)?.kind, "edit");
assert.equal((document.querySelector("textarea.composer__input") as HTMLTextAreaElement).value, "main draft survives editing");
assert.equal(enqueues, 1, "saving an edit never adds a new message");
assert.equal(document.querySelector(".composer-context__item"), attachmentCard, "saved queue edits do not clear main attachments");
assert.equal(document.querySelector(".composer-wrap")?.getAttribute("data-queue-editing"), null);
await act(async () => { (document.querySelector('.inbox-queue__row[data-item-id="queue-save"] button[aria-label="Edit"]') as HTMLButtonElement).click(); await flush(); });
assert.ok(document.querySelector(".inbox-queue__editor"));
await act(async () => {
  document.querySelector(".inbox-queue__editor textarea")!.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  await flush();
});
assert.equal(document.querySelector(".inbox-queue__editor"), null);
assert.equal(document.querySelector(".composer-wrap")?.getAttribute("data-queue-editing"), null);
assert.ok(!document.querySelector(".inbox-queue")!.textContent!.includes("Your edits have been kept"), "unchanged cancellation leaves no draft banner");
assert.equal(cancelled, undefined, "Escape exits editing without stopping the task");
await act(async () => { (document.querySelector('.inbox-queue__row[data-item-id="queue-save"] button[aria-label="Edit"]') as HTMLButtonElement).click(); await flush(); });
await act(async () => {
  const saveButton = document.querySelector(".inbox-queue__editor .btn--primary") as HTMLButtonElement;
  saveButton.focus();
  saveButton.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  await flush();
});
assert.equal(Boolean(document.querySelector(".inbox-queue__editor")), false, "Escape also exits when an editor action has keyboard focus");
assert.equal(document.querySelector(".composer-context__item"), attachmentCard, "cancelling restores the same attachment");
assert.equal((document.querySelector("textarea.composer__input") as HTMLTextAreaElement).value, "main draft survives editing");
failSave = true;
await act(async () => { (document.querySelector('.inbox-queue__row[data-item-id="queue-save"] button[aria-label="Edit"]') as HTMLButtonElement).click(); await flush(); });
await act(async () => {
  document.querySelector(".inbox-queue__editor textarea")!.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
  await flush();
});
assert.equal(document.querySelector('.inbox-queue__editor [role="alert"]')?.textContent, "This message was edited elsewhere. Your changes were kept.");
assert.equal(document.querySelector(".composer-wrap")?.getAttribute("data-queue-editing"), "true", "failed save keeps edit mode and original text");
assert.equal((document.querySelector(".inbox-queue__editor textarea") as HTMLTextAreaElement).value, textarea.value);
failSave = false;
await act(async () => {
  const reload = [...document.querySelectorAll<HTMLButtonElement>(".inbox-queue__editor button")].find(button => button.textContent === "Load latest version")!;
  reload.click(); await flush();
});
departOnSave = true;
await act(async () => {
  document.querySelector(".inbox-queue__editor textarea")!.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
  await flush();
});
assert.equal(document.querySelector('.inbox-queue__row[data-item-id="queue-save"]'), null);
assert.ok(document.querySelector('.inbox-queue__departed textarea'), "dispatch or deletion cannot unmount unsaved text");
assert.ok(document.querySelector('.inbox-queue__departed [role="alert"]'));
await act(async () => { (document.querySelector(".inbox-queue__running button") as HTMLButtonElement).click(); await flush(); });
assert.deepEqual(cancelled, [], "stop during editing leaves the queue intact");
assert.ok(document.querySelector('.inbox-queue__departed textarea'), "stop does not discard edit text");
await act(async () => {
  document.querySelector(".inbox-queue__editor textarea")!.dispatchEvent(new window.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  await flush();
});
await act(async () => { (document.querySelector(".composer__btn--stop") as HTMLButtonElement).click(); await flush(); });
assert.deepEqual(cancelled, [], "stop leaves the durable queue intact");
otherHost = true;
await view.rerender({ inboxHostId: "second-host", inboxWorkspace: "/repo" });
await act(async () => { await flush(); });
assert.ok(document.querySelector('.inbox-queue__row[data-item-id="other-host"]'), "a different host may start at a lower revision for the same session path");
assert.equal(document.querySelector('.inbox-queue__row[data-item-id="queue-save"]'), null, "the previous host's queue cannot leak into the new host");
await act(async () => { view.root.unmount(); });
dom.window.close();
console.log("PASS composer queue: default follow-up, explicit guide, in-place editing, draft preservation, stop");
