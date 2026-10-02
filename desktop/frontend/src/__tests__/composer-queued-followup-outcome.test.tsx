// A busy-window submit (the desktop thinks idle, the serve is still finishing)
// is answered with a durable queue receipt. The composer must show the queue
// entry immediately and treat the message as delivered-to-queue, never as a
// failure — local busy sends behave the same way.
import assert from "node:assert/strict";
import { act } from "react";
import { installDom, installBridgeApp, renderComposer } from "./composerInboxHarness";

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0));

async function paste(text: string) {
  await act(async () => {
    const event = new window.Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "clipboardData", { value: { files: [], items: [], types: ["text/plain"], getData: () => text } });
    document.querySelector("textarea.composer__input:not([aria-hidden=true])")!.dispatchEvent(event);
    await flush();
  });
}
async function send() {
  await act(async () => { (document.querySelector(".composer__btn--send") as HTMLButtonElement).click(); await flush(); });
}
const emptySnapshot = { revision: 1, paused: false, recovered: false, items: [], itemsCount: 0, bytes: 0, maxItems: 64, maxBytes: 1024 };
const queuedSnapshot = {
  revision: 2, paused: false, recovered: false, itemsCount: 1, bytes: 12, maxItems: 64, maxBytes: 1024,
  items: [{ id: "queued-1", intent: "followup", state: "queued", preview: "queued while busy" }],
};
const queuedError = () => Object.assign(new Error("reasonix_error:queued_followup"), {
  code: "business",
  data: { method: "SubmitRemoteTabWithSubmission", queuedFollowup: { itemId: "queued-1", disposition: "queued_followup", position: 0, paused: false } },
});

installDom();
installBridgeApp({
  InboxQueueForTarget: async () => ({ outcome: "applied", snapshot: emptySnapshot }),
  InboxSnapshot: async () => queuedSnapshot,
});
await renderComposer({
  running: false,
  inboxSessionPath: "session-a",
  onSend: async () => { throw queuedError(); },
});
await paste("queued while busy");
await send();

// The queue strip reconciles through the snapshot refresh; let it settle.
await act(async () => { await flush(); await flush(); await new Promise(r => setTimeout(r, 60)); await flush(); });
const body = document.body.textContent ?? "";
assert.ok(body.includes("queued while busy"), "the queued message is visible in the pending queue");
assert.equal(body.includes("Send failed"), false, "a queued submit is not reported as a failed send");
assert.equal(body.includes("Refresh the session"), false, "a queued submit never asks the user to refresh");
assert.equal((document.querySelector("textarea.composer__input:not([aria-hidden=true])") as HTMLTextAreaElement).value, "",
  "the composer clears once the message is queued");

console.log("composer queued follow-up outcome: busy submits surface as a visible queue entry");
process.exit(0);
