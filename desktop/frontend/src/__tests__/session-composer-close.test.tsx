import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useSessionComposerLifecycle } from "../app-runtime/useSessionComposerLifecycle";
import { emptySessionInput, resumeSessionComposerEditing, useSessionComposerPersistence } from "../lib/sessionComposerPersistence";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { installDesktopHostStub } from "./desktopHostStub";
import { SessionInputRecovery } from "../components/SessionInputRecovery";
import { LocaleProvider } from "../lib/i18n";
import type { SessionRef } from "../lib/sessionRef";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}
const tick = () => new Promise<void>(done => setImmediate(done));

test("closing formal session input preserves drafts without depending on attachment capabilities", async t => {
  const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const root = createRoot(document.getElementById("root")!);
  const backend = makeSessionUIMock(async () => {});
  let captures = 0, writes = 0, failSave = false;
  const save: typeof backend.SaveSessionComposerState = async request => {
    writes++;
    if (failSave) throw Error("disk full");
    return backend.SaveSessionComposerState(request);
  };
  const host = installDesktopHostStub({ ...backend, SaveSessionComposerState: save,
    CaptureAttachmentTarget: async () => { captures++; throw Error("unsupported: attachments-v2"); },
    AttachDroppedForTarget: async () => { throw Error("must not activate attachments while loading input"); },
  });
  const ref = (sessionId: string) => ({ hostId: "local", sessionId });
  let editor!: ReturnType<typeof useSessionComposerPersistence>;
  function Probe({ session }: { session: string }) {
    useSessionComposerLifecycle();
    editor = useSessionComposerPersistence(ref(session), `tab-${session}`);
    return <LocaleProvider><SessionInputRecovery input={editor} /></LocaleProvider>;
  }
  const paint = (session: string) => act(async () => { root.render(React.createElement(Probe, { session })); });
  const patch = (text: string) => {
    const target = editor.target!;
    target.onPatch!(target.draftId, target.generation, { text });
  };
  const attachmentError = (message = "unsupported: attachments-v2") => {
    const target = editor.target!;
    target.onTaskError!(target.draftId, target.generation, message);
  };
  const close = () => window.__reasonixFlushSessionDraft!();
  const saved = async (session: string) => JSON.parse((await backend.GetSessionComposerState(ref(session))).contentJson);
  async function resume() { await act(async () => resumeSessionComposerEditing()); }

  try {
    await t.test("a blank window closes without saving or capturing attachments", async () => {
      await paint("blank");
      const before = writes;
      await act(async () => close());
      assert.equal(writes, before);
      assert.equal(captures, 0);
      await resume();
    });

    await t.test("a failed attachment on blank input stays visible but does not veto close", async () => {
      await paint("blank-warning");
      await act(async () => attachmentError("unsupported: attachments-v2\nTrace: attachment-target"));
      assert.equal(editor.error, undefined);
      assert.equal(document.querySelector('.user-error__summary')!.textContent, "unsupported: attachments-v2");
      assert.equal(document.querySelector('.user-error__detail'), null, "raw diagnostics start collapsed");
      await act(async () => document.querySelector<HTMLButtonElement>('.user-error__toggle')!.click());
      assert.match(document.querySelector('.user-error__detail')!.textContent!, /Trace: attachment-target/);
      assert.doesNotMatch(document.body.textContent!, /Retry to recover saving/);
      assert.equal(editor.target!.initial.text, "");
      try {
        await act(async () => close());
        await act(async () => document.querySelector<HTMLButtonElement>('[role="alert"] > button')!.click());
        assert.equal(document.querySelector('[role="alert"]'), null);
      }
      finally { await act(async () => editor.retry()); await resume(); }
    });

    await t.test("reopening stored external references never requires a live attachment controller", async () => {
      const content = { ...emptySessionInput(), workspaceRefs: [
        { path: "__reasonix_external_folder/hash/folder", displayPath: "/external/folder", isDir: true },
      ] };
      await backend.SaveSessionComposerState({ ref: ref("external"), expectedRevision: "0", contentVersion: 1, contentJson: JSON.stringify(content) });
      await paint("external");
      // Drain the old eager dynamic import too, so the regression observes its failure.
      await act(async () => { await import("../lib/attachmentSubmit"); await tick(); });
      const external = editor;
      try {
        assert.equal(external.error, undefined);
        assert.equal(external.blocked, false);
        assert.deepEqual(external.target!.initial.workspaceRefs, content.workspaceRefs);
        await paint("blank-after-external");
        await act(async () => close());
        assert.deepEqual(await saved("external"), content);
        assert.equal(captures, 0);
      } finally { await act(async () => external.retry()); await resume(); }
    });

    await t.test("an attachment warning cannot prevent real input from being saved", async () => {
      await paint("text-warning");
      await act(async () => { patch("keep this unsent text"); attachmentError(); });
      try {
        await act(async () => close());
        assert.equal((await saved("text-warning")).text, "keep this unsent text");
      } finally { await act(async () => editor.retry()); await resume(); }
    });

    await t.test("a true save failure blocks close even when the visible page is blank", async () => {
      await paint("hidden-unsaved");
      const hidden = editor;
      failSave = true;
      await act(async () => { patch("never lose this draft"); attachmentError(); });
      await paint("blank-with-hidden-draft");
      try {
        await act(async () => { await assert.rejects(close(), /disk full/); });
        assert.equal(editor.target!.initial.text, "");
        assert.equal(editor.blocked, false, "failed close resumes the visible editor");
        await paint("hidden-unsaved");
        assert.equal(editor.target!.initial.text, "never lose this draft");
        assert.match(document.body.textContent!, /Retry to recover saving/, "save failure takes priority over attachment feedback");
        assert.notEqual((await saved("hidden-unsaved")).text, "never lose this draft");
      } finally { failSave = false; await act(async () => hidden.retry()); }
      await paint("blank-with-hidden-draft");
      await act(async () => close());
      assert.equal((await saved("hidden-unsaved")).text, "never lose this draft");
      await resume();
    });

    await t.test("close waits for attachment completion and saves the resulting image-only draft", async () => {
      await paint("pending-attachment");
      const release = deferred(), target = editor.target!;
      const attachments = [{ path: "draft:temporary", recoveryPath: ".reasonix/attachments/image.png", draftId: "temporary" }];
      target.trackTask!(target.draftId, target.generation, release.promise.then(() => {
        target.onPatch!(target.draftId, target.generation, { attachments });
      }));
      let finished = false;
      let closing!: Promise<void>;
      await act(async () => { closing = close().then(() => { finished = true; }); await tick(); });
      assert.equal(finished, false);
      await act(async () => { release.resolve(); await closing; });
      assert.deepEqual((await saved("pending-attachment")).attachments, [{ path: attachments[0].recoveryPath, recoveryPath: attachments[0].recoveryPath }]);
      await resume();
    });

    await t.test("close cannot skip a pending load that fails to read persisted input", async () => {
      const entered = deferred(), release = deferred();
      host.commands.GetSessionComposerState = async () => { entered.resolve(); await release.promise; throw Error("input read failed"); };
      await paint("pending-read");
      await entered.promise;
      let finished = false;
      let closing!: Promise<unknown>;
      await act(async () => { closing = close().then(() => { finished = true; }, error => error); await tick(); });
      try {
        assert.equal(finished, false, "must await the initial input read before allowing close");
        await act(async () => { release.resolve(); });
        assert.match(String(await closing), /input read failed/);
      } finally {
        await act(async () => { release.resolve(); await closing; });
        host.commands.GetSessionComposerState = backend.GetSessionComposerState;
        await act(async () => editor.retry()); await resume();
      }
    });

    await t.test("close waits for recovery to discover input that still needs saving", async () => {
      await paint("pending-recovery");
      await act(async () => patch("this window input"));
      await act(async () => close());
      await resume();
      const current = await backend.GetSessionComposerState(ref("pending-recovery"));
      await backend.SaveSessionComposerState({ ref: current.ref, expectedRevision: current.revision, contentVersion: 1, contentJson: '{"text":"other window input"}' });
      const entered = deferred(), release = deferred();
      host.commands.GetSessionComposerState = async (value: SessionRef) => { entered.resolve(); await release.promise; return backend.GetSessionComposerState(value); };
      let recovery!: Promise<void>, closing!: Promise<void>, finished = false;
      await act(async () => { recovery = editor.retry(); await entered.promise; });
      await act(async () => { closing = close().then(() => { finished = true; }); await tick(); });
      try { assert.equal(finished, false, "a clean version can become dirty during recovery"); }
      finally {
        await act(async () => { release.resolve(); await recovery; await closing; });
        host.commands.GetSessionComposerState = backend.GetSessionComposerState;
        await resume();
      }
      assert.equal((await saved("pending-recovery")).text, "this window input");
    });
  } finally {
    await act(async () => root.unmount());
    host.uninstall(); dom.window.close();
  }
});
