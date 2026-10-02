import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { installDom, installBridgeApp } from "./composerInboxHarness";
import { useInboxQueueEditor } from "../lib/useInboxQueueEditor";
import { inboxQueueDrafts } from "../lib/inboxQueueDrafts";
import { queueActionAnchor, queueMoveAnchor, type InboxQueueRequest } from "../lib/inboxQueueCommands";
import { createInboxQueuePreview } from "../lib/inboxQueuePreview";
import type { InboxTarget } from "../lib/pendingFollowup";

const dom = installDom();
globalThis.sessionStorage = dom.window.sessionStorage;
const target: InboxTarget = { tabId: "tab-a", sessionPath: "session-a", generation: 7, selection: 3, remote: true };
const fixture = createInboxQueuePreview();
const requests: InboxQueueRequest[] = [];
let loseSaveReply = false;
installBridgeApp({
  CaptureInboxTarget: async () => target,
  InboxQueueForTarget: async (captured: InboxTarget, request: InboxQueueRequest) => {
    assert.deepEqual(captured, target);
    requests.push(request);
    const result = await fixture.command(captured, request);
    if (request.kind === "edit" && loseSaveReply) throw new Error("reply lost after commit");
    return result;
  },
});
let editor!: ReturnType<typeof useInboxQueueEditor>;
function Harness({ scope }: { scope: string }) {
  editor = useInboxQueueEditor(scope, target.tabId, target.sessionPath, () => {}, () => {});
  return <p>{editor.drafts.edit?.value}</p>;
}
const root = createRoot(document.getElementById("root")!);
const render = (scope: string) => act(async () => { root.render(<Harness key={scope} scope={scope} />); });
await render("queue-test-a");
await act(async () => { await editor.edit("queue-layout"); });
await act(async () => { editor.close(); });
assert.equal(editor.drafts.edit, undefined, "opening and cancelling without changes does not create a retained draft");
assert.equal(editor.open, false);
await act(async () => { await editor.edit("queue-save"); });
const original = editor.drafts.edit!;
assert.ok(original.value.includes("\n完成后验证"), "read includes the full multiline body");
assert.equal(original.references.length, 2);
const longText = "  保留空白\n" + "完整正文".repeat(200) + "\n结束  ";
await act(async () => { editor.update(longText); editor.close(); });
await render("queue-test-b");
assert.equal(editor.drafts.edit, undefined, "draft is scoped to its original session");
await render("queue-test-a");
assert.equal(editor.drafts.edit?.value, longText, "remount retains unsaved input");
await act(async () => { await editor.mutate({ kind: "pause", paused: true }); await editor.save(); });
assert.equal(editor.drafts.edit, undefined);
assert.equal(fixture.snapshot().paused, true, "saving does not resume the queue");
assert.deepEqual(fixture.snapshot().items.map(item => item.id), ["queue-layout", "queue-save", "queue-tests"]);
assert.equal(fixture.snapshot().items[1].preview, longText);

await act(async () => { await editor.edit("queue-save"); editor.update("my conflicting edits"); });
await fixture.command(target, { kind: "edit", itemId: "queue-save", text: "another window", contentVersion: editor.drafts.edit!.contentVersion });
await act(async () => { await editor.save(); });
assert.equal(editor.drafts.edit?.value, "my conflicting edits");
assert.equal(editor.drafts.edit?.reason, "content_changed");
await act(async () => { await editor.reload(); });
assert.equal(editor.drafts.edit?.value, "another window");
assert.equal(editor.drafts.recovered.at(-1)?.value, "my conflicting edits");

await act(async () => { editor.update("save committed but reply lost"); });
loseSaveReply = true;
const before = requests.length;
await act(async () => { await editor.save(); });
assert.deepEqual(requests.slice(before).map(r => r.kind), ["edit", "snapshot"], "ambiguous save reconciles once without replay");
assert.equal(editor.drafts.edit?.reason, "unconfirmed");
assert.equal(editor.drafts.edit?.value, "save committed but reply lost");
let restored: string | undefined;
await act(async () => { restored = editor.recover(); });
assert.equal(restored, "save committed but reply lost");
assert.equal(editor.drafts.edit, undefined);
assert.equal(editor.drafts.recovered.at(-1)?.value, restored);

// Pointer and keyboard actions share exactly the same stable anchor contract.
assert.equal(queueMoveAnchor(["a", "b", "c"], "a", "b"), "c");
assert.equal(queueActionAnchor(["a", "b", "c"], "a", "down"), "c");
assert.equal(queueMoveAnchor(["a", "b", "c"], "a", "c"), null);
assert.equal(queueActionAnchor(["a", "b", "c"], "c", "first"), "a");
assert.equal(queueMoveAnchor(["a", "b", "c"], "a", "missing"), undefined);
await act(async () => { inboxQueueDrafts.set("queue-test-a", { recovered: [] }); root.unmount(); });
dom.window.close();
console.log("PASS queue editor: full body, remount, pause, conflict, recovery and ambiguous save");
