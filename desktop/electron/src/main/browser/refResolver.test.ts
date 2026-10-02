import assert from "node:assert/strict";
import { test } from "node:test";
import { FakeFrame, FakePage } from "./fakeGuestViews.js";
import { locateRef, resolveRef } from "./refResolver.js";
import { browserFailure } from "./errors.js";
import type { DocumentBinding } from "./documents.js";

test("reference lookups preserve typed runtime failures for main and child frames", async () => {
  for (const child of [false, true]) for (const kind of ["page_not_ready", "script_runtime_error", "cancelled"] as const) {
    for (const operation of [locateRef, (page: FakePage, binding: DocumentBinding, ref: string) => resolveRef(page, binding, ref, false)]) {
      const failure = browserFailure(kind, "protocol preparation failed");
      const page = new FakePage(1, () => { throw failure; });
      const frame = child ? new FakeFrame(2, "https://child.test", () => { throw failure; }) : page.mainFrame;
      if (child) { frame.parent = page.mainFrame; page.mainFrame.children.push(frame); }
      page.debugger.attached = true;
      page.debugger.respond = () => { throw failure; };
      const prefix = child ? "f1" : "";
      const binding: DocumentBinding = { tabId: "tab", epoch: 1, snapshotId: "snapshot", frames: [{ prefix, frameTreeNodeId: frame.frameTreeNodeId, docId: "document" }] };
      await assert.rejects(operation(page, binding, `${prefix}e1`), error => error === failure,
        `${child ? "child" : "main"} ${kind} must not be relabeled as an expired ref`);
    }
  }
});

test("an expired page registry still produces the stale-reference contract", async () => {
  const page = new FakePage(1, () => ({ ok: false, reason: "stale" }));
  const binding: DocumentBinding = { tabId: "tab", epoch: 1, snapshotId: "old", frames: [{ prefix: "", frameTreeNodeId: page.mainFrame.frameTreeNodeId, docId: "old" }] };
  await assert.rejects(locateRef(page, binding, "e1"), { code: -32010, data: { kind: "stale_document" } });
  await assert.rejects(resolveRef(page, binding, "e1", false), { code: -32010, data: { kind: "stale_document" } });
});
