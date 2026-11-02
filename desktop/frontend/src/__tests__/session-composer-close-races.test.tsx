import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useSessionComposerLifecycle } from "../app-runtime/useSessionComposerLifecycle";
import { resumeSessionComposerEditing, sendPersistedComposer, useSessionComposerPersistence } from "../lib/sessionComposerPersistence";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { installDesktopHostStub } from "./desktopHostStub";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}
const tick = () => new Promise<void>(done => setImmediate(done));
type Editor = ReturnType<typeof useSessionComposerPersistence>;

test("close drains all owners and retries retained drafts after storage recovery", async t => {
  const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const root = createRoot(document.getElementById("root")!);
  const backend = makeSessionUIMock(async () => {});
  const host = installDesktopHostStub({ ...backend });
  const ref = (sessionId: string) => ({ hostId: "local", sessionId });
  let a!: Editor, b!: Editor;
  function Probe({ prefix }: { prefix: string }) {
    useSessionComposerLifecycle();
    a = useSessionComposerPersistence(ref(`${prefix}-a`), `${prefix}-a`);
    b = useSessionComposerPersistence(ref(`${prefix}-b`), `${prefix}-b`);
    return null;
  }
  const paint = (prefix: string) => act(async () => { root.render(React.createElement(Probe, { prefix })); });
  const patch = (editor: Editor, text: string) => {
    const target = editor.target!;
    target.onPatch!(target.draftId, target.generation, { text });
  };
  const track = (editor: Editor, task: Promise<unknown>) => {
    const target = editor.target!;
    return target.trackTask!(target.draftId, target.generation, task);
  };
  const canEdit = (editor: Editor) => editor.target!.canEdit!(editor.target!.draftId, editor.target!.generation);
  const close = () => window.__reasonixFlushSessionDraft!();
  const saved = async (session: string) => JSON.parse((await backend.GetSessionComposerState(ref(session))).contentJson);
  const resume = () => act(async () => resumeSessionComposerEditing());

  try {
    await t.test("close blocks new operations but preserves accepted attachment completion", async () => {
      await paint("admission");
      const release = deferred();
      const owner = a;
      track(a, release.promise.then(() => patch(owner, "accepted attachment result")));
      let closing!: Promise<void>;
      await act(async () => { closing = close(); await tick(); });
      try {
        assert.equal(a.blocked, true);
        assert.equal(canEdit(a), false, "native drop and paste must see the close barrier");
        assert.equal(canEdit(b), false);
        await act(async () => b.setGoalDraft(true));
        assert.notEqual(b.goalDraft, true, "a new mode edit must not enter during close");
      } finally { await act(async () => { release.resolve(); await closing; }); }
      assert.equal((await saved("admission-a")).text, "accepted attachment result");
      assert.equal(canEdit(a), false, "stay blocked until the shell resumes editing");
      await resume();
      assert.equal(canEdit(a), true);
    });

    for (const taskFinished of [false, true]) await t.test(`close revisits an earlier owner with ${taskFinished ? "unsaved input" : "a pending task"} after a later save`, async () => {
      await resume();
      const prefix = `late-${taskFinished}`;
      await paint(prefix);
      const saving = deferred(), releaseSave = deferred(), releaseTask = deferred();
      host.commands.SaveSessionComposerState = async (request: Parameters<typeof backend.SaveSessionComposerState>[0]) => {
        if (request.ref.sessionId === `${prefix}-b`) { saving.resolve(); await releaseSave.promise; }
        return backend.SaveSessionComposerState(request);
      };
      await act(async () => patch(b, "later owner"));
      let closing!: Promise<void>, finished = false;
      await act(async () => { closing = close().then(() => { finished = true; }); await saving.promise; });
      const owner = a;
      const task = track(a, releaseTask.promise.then(() => patch(owner, "late retained result")));
      try {
        if (taskFinished) await act(async () => { releaseTask.resolve(); await task; });
        await act(async () => { releaseSave.resolve(); await tick(); });
        if (!taskFinished) assert.equal(finished, false, "a late task on an already visited owner must still block close");
      } finally {
        await act(async () => { releaseTask.resolve(); releaseSave.resolve(); await closing; });
        host.commands.SaveSessionComposerState = backend.SaveSessionComposerState;
      }
      assert.equal((await saved(`${prefix}-a`)).text, "late retained result");
      assert.equal((await saved(`${prefix}-b`)).text, "later owner");
      await resume();
    });

    await t.test("retrying close reconciles a hidden draft and still vetoes persistent save failure", async () => {
      await resume();
      await paint("retry");
      const hidden = a;
      let failSave = true, writes = 0;
      host.commands.SaveSessionComposerState = async (request: Parameters<typeof backend.SaveSessionComposerState>[0]) => {
        writes++;
        if (failSave) throw Error("storage unavailable");
        return backend.SaveSessionComposerState(request);
      };
      // Exhaust the automatic retry first, without depending on wall-clock timing.
      t.mock.timers.enable({ apis: ["setTimeout"] });
      await act(async () => { patch(hidden, "retain hidden input"); t.mock.timers.tick(250); await tick(); });
      await paint("blank");
      try {
        const before = writes;
        await act(async () => { await assert.rejects(close(), /storage unavailable/); });
        assert.equal(writes, before + 1, "one bounded save retry per explicit close");
        assert.equal(canEdit(a), true, "failed close resumes the visible blank input");
        assert.equal(a.target!.initial.text, "");
        assert.notEqual((await saved("retry-a")).text, "retain hidden input");
        failSave = false;
        const beforeRecovery = writes;
        await act(async () => close());
        assert.equal(writes, beforeRecovery + 1);
        assert.equal((await saved("retry-a")).text, "retain hidden input");
      } finally {
        failSave = false;
        await act(async () => hidden.retry());
        host.commands.SaveSessionComposerState = backend.SaveSessionComposerState;
        t.mock.timers.reset();
        await resume();
      }
    });

    await t.test("retrying close can recover failed reads without replacing durable input", async () => {
      await backend.SaveSessionComposerState({ ref: ref("read-a"), expectedRevision: "0", contentVersion: 1, contentJson: '{"text":"durable unread input"}' });
      let reads = 0;
      host.commands.GetSessionComposerState = async () => { reads++; throw Error("read unavailable"); };
      await paint("read");
      try {
        const before = reads;
        await act(async () => { await assert.rejects(close(), /read unavailable/); });
        assert.equal(reads, before + 1, "do not loop on unavailable storage");
        assert.equal(a.blocked, true, "unread input must remain protected after failed close");
        host.commands.GetSessionComposerState = backend.GetSessionComposerState;
        await act(async () => close());
        assert.equal(a.target!.initial.text, "durable unread input");
        assert.equal((await saved("read-a")).text, "durable unread input");
      } finally {
        host.commands.GetSessionComposerState = backend.GetSessionComposerState;
        await act(async () => { await a.retry(); await b.retry(); });
        await resume();
      }
    });

    await t.test("close recovery never resends or releases an ambiguous submission", async () => {
      await paint("unknown");
      await act(async () => patch(a, "pending submission input"));
      let sends = 0;
      await act(async () => {
        await assert.rejects(sendPersistedComposer("unknown-a", "pending submission input", "pending submission input", "unknown-send", async () => {
          sends++;
          throw Object.assign(Error("reply lost"), { data: { submissionOutcome: "unknown" } });
        }));
      });
      host.commands.GetSessionComposerState = async () => { throw Error("temporary read failure"); };
      await act(async () => { await assert.rejects(a.retry(), /temporary read failure/); });
      host.commands.GetSessionComposerState = backend.GetSessionComposerState;
      await act(async () => close());
      const state = await backend.GetSessionComposerState(ref("unknown-a"));
      assert.equal(sends, 1);
      assert.equal(state.submissionId, "unknown-send");
      assert.equal(JSON.parse(state.contentJson).text, "pending submission input");
      await resume();
      assert.equal(canEdit(a), false, "ambiguous submission remains locked after close resumes");
    });
  } finally {
    await act(async () => { resumeSessionComposerEditing(); root.unmount(); });
    host.uninstall(); dom.window.close();
  }
});
