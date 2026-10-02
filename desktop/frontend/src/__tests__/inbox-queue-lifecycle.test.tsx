import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { installDom, installBridgeApp } from "./composerInboxHarness";
import { useInboxQueueEditor } from "../lib/useInboxQueueEditor";
import { createInboxQueuePreview } from "../lib/inboxQueuePreview";
import type { InboxQueueRequest } from "../lib/inboxQueueCommands";
import type { InboxTarget } from "../lib/pendingFollowup";

const dom = installDom();
globalThis.sessionStorage = dom.window.sessionStorage;
const root = createRoot(document.getElementById("root")!);
let editor!: ReturnType<typeof useInboxQueueEditor>;
let applied = 0;
function Harness({ scope }: { scope: string }) {
  editor = useInboxQueueEditor(scope, "tab-a", "session-a", () => { applied++; }, () => {});
  return <p>{editor.drafts.edit?.value}</p>;
}
const render = (scope: string) => act(async () => { root.render(<Harness key={scope} scope={scope} />); });
function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}
const target: InboxTarget = { tabId: "tab-a", sessionPath: "session-a", generation: 1, selection: 1, remote: true };
const failures: string[] = [];
async function check(name: string, test: () => Promise<void>) {
  try { await test(); console.log(`PASS ${name}`); }
  catch (error) { failures.push(`${name}: ${String(error)}`); }
}

await check("late save must not clear a remounted editor's new draft", async () => {
  const fixture = createInboxQueuePreview();
  const committed = deferred(), reply = deferred();
  installBridgeApp({
    CaptureInboxTarget: async () => target,
    InboxQueueForTarget: async (captured: InboxTarget, request: InboxQueueRequest) => {
      const result = await fixture.command(captured, request);
      if (request.kind === "edit") { committed.resolve(); await reply.promise; }
      return result;
    },
  });
  await render("save-a");
  await act(async () => { await editor.edit("queue-save"); editor.update("submitted text"); });
  let save!: Promise<void>;
  await act(async () => { save = editor.save(); await committed.promise; });
  await render("save-b");
  await render("save-a");
  await act(async () => { editor.update("newer text after returning"); });
  const before = applied;
  await act(async () => { reply.resolve(); await save; });
  assert.equal(editor.drafts.edit?.value, "newer text after returning");
  assert.equal(applied, before, "unmounted owner cannot publish snapshots");
});

await check("target capture resolving after unmount must not issue a command", async () => {
  const fixture = createInboxQueuePreview();
  const captured = deferred(), release = deferred();
  const requests: InboxQueueRequest[] = [];
  installBridgeApp({
    CaptureInboxTarget: async () => { captured.resolve(); await release.promise; return target; },
    InboxQueueForTarget: async (binding: InboxTarget, request: InboxQueueRequest) => { requests.push(request); return fixture.command(binding, request); },
  });
  await render("capture-a");
  let pause!: Promise<void>;
  await act(async () => { pause = editor.mutate({ kind: "pause", paused: true }); await captured.promise; });
  await render("capture-b");
  await act(async () => { release.resolve(); await pause; });
  assert.equal(requests.length, 0);
  assert.equal(fixture.snapshot().paused, false);
});

await check("late read cannot replace an edit opened by the new owner", async () => {
  const fixture = createInboxQueuePreview();
  const read = deferred(), reply = deferred();
  installBridgeApp({
    CaptureInboxTarget: async () => target,
    InboxQueueForTarget: async (binding: InboxTarget, request: InboxQueueRequest) => {
      const result = await fixture.command(binding, request);
      if (request.kind === "read" && request.itemId === "queue-save") { read.resolve(); await reply.promise; }
      return result;
    },
  });
  await render("read-a");
  let pending!: Promise<void>;
  await act(async () => { pending = editor.edit("queue-save"); await read.promise; });
  await render("read-b");
  await render("read-a");
  await act(async () => { await editor.edit("queue-tests"); editor.update("new owner's draft"); });
  const before = applied;
  await act(async () => { reply.resolve(); await pending; });
  assert.equal(editor.drafts.edit?.id, "queue-tests");
  assert.equal(editor.drafts.edit?.value, "new owner's draft");
  assert.equal(applied, before);
});

await check("late failed save does not reconcile or overwrite a replacement draft", async () => {
  const fixture = createInboxQueuePreview();
  const started = deferred(), reply = deferred();
  const requests: InboxQueueRequest[] = [];
  installBridgeApp({
    CaptureInboxTarget: async () => target,
    InboxQueueForTarget: async (binding: InboxTarget, request: InboxQueueRequest) => {
      requests.push(request);
      if (request.kind === "edit") { started.resolve(); await reply.promise; throw new Error("reply lost"); }
      return fixture.command(binding, request);
    },
  });
  await render("failure-a");
  await act(async () => { await editor.edit("queue-save"); editor.update("submitted"); });
  let pending!: Promise<void>;
  await act(async () => { pending = editor.save(); await started.promise; });
  await render("failure-b");
  await render("failure-a");
  await act(async () => { editor.update("preserve newer input"); });
  await act(async () => { reply.resolve(); await pending; });
  assert.equal(editor.drafts.edit?.value, "preserve newer input");
  assert.equal(editor.drafts.edit?.reason, undefined);
  assert.equal(requests.filter(request => request.kind === "snapshot").length, 0);
});

await check("load latest reacquires current selection instead of reusing stale target", async () => {
  const fixture = createInboxQueuePreview();
  let selection = 1;
  installBridgeApp({
    CaptureInboxTarget: async () => ({ ...target, selection }),
    InboxQueueForTarget: async (binding: InboxTarget, request: InboxQueueRequest) => binding.selection !== selection
      ? { outcome: "unavailable", reason: "session_changed", snapshot: fixture.snapshot("session-a") }
      : fixture.command(binding, request),
  });
  await render("reload-a");
  await act(async () => { await editor.edit("queue-save"); editor.update("keep this local edit"); });
  selection = 2;
  await act(async () => { await editor.save(); });
  assert.equal(editor.drafts.edit?.reason, "session_changed");
  await act(async () => { await editor.reload(); });
  assert.equal(editor.drafts.edit?.reason, undefined);
  assert.equal(editor.drafts.edit?.target.selection, 2);
  assert.equal(editor.drafts.recovered.at(-1)?.value, "keep this local edit");
});

await act(async () => { root.unmount(); });
dom.window.close();
assert.deepEqual(failures, []);
