import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { sendPersistedComposer, useSessionComposerPersistence } from "../lib/sessionComposerPersistence";
import type { SessionRef } from "../lib/sessionRef";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
}

test("composer submission boundaries", async t => {
  const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const root = createRoot(document.getElementById("root")!);
  const backend = makeSessionUIMock(async () => {});
  const host = installDesktopHostStub({ ...backend });
  let editor!: ReturnType<typeof useSessionComposerPersistence>;
  function Probe({ session }: { session: string }) {
    editor = useSessionComposerPersistence({ hostId: "local", sessionId: session }, "race-tab");
    return null;
  }
  const paint = (session: string) => act(async () => {
    root.render(<Probe session={session} />);
    await new Promise(resolve => setTimeout(resolve, 0));
  });
  const patch = (text: string) => {
    const target = editor.target!;
    target.onPatch!(target.draftId, target.generation, { text });
  };
  try {
    await t.test("registration freezes the captured input before the RPC returns", async () => {
      await paint("begin-race");
      await act(async () => patch("submitted text"));
      const entered = deferred(), release = deferred();
      host.commands.BeginSessionComposerSubmission = async (...args: Parameters<typeof backend.BeginSessionComposerSubmission>) => {
        const result = await backend.BeginSessionComposerSubmission(...args);
        entered.resolve(); await release.promise; return result;
      };
      let pending!: Promise<unknown>;
      try {
        await act(async () => {
          pending = sendPersistedComposer("race-tab", "submitted text", "submitted text", "begin-race-id", async () => {});
          await entered.promise;
        });
        assert.equal(editor.blocked, true);
        await act(async () => patch("must not silently replace captured input"));
        assert.equal(editor.target!.initial.text, "submitted text");
      } finally {
        await act(async () => { release.resolve(); await pending; });
        host.commands.BeginSessionComposerSubmission = backend.BeginSessionComposerSubmission;
      }
    });

    await t.test("late acceptance preserves edits made after receipt reconciliation", async () => {
      const ref: SessionRef = { hostId: "local", sessionId: "receipt-race" };
      await paint(ref.sessionId);
      await act(async () => patch("first input"));
      const sent = deferred(), response = deferred();
      let pending!: Promise<unknown>;
      await act(async () => {
        pending = sendPersistedComposer("race-tab", "first input", "first input", "receipt-race-id", async () => {
          await backend.CompleteSessionComposerSubmission(ref, "receipt-race-id", "accepted");
          sent.resolve(); await response.promise;
        });
        await sent.promise;
      });
      await act(async () => editor.retry());
      assert.equal(editor.blocked, false);
      await act(async () => patch("new unsent input"));
      await act(async () => { response.resolve(); await pending; });
      assert.equal(editor.target!.initial.text, "new unsent input");
      await act(async () => editor.retry());
      assert.equal(JSON.parse((await backend.GetSessionComposerState(ref)).contentJson).text, "new unsent input");
    });

    await t.test("departing and returning cannot revive an old send intent", async () => {
      await paint("navigation-race");
      await act(async () => patch("do not send after leaving"));
      const entered = deferred(), release = deferred();
      host.commands.BeginSessionComposerSubmission = async (...args: Parameters<typeof backend.BeginSessionComposerSubmission>) => {
        const result = await backend.BeginSessionComposerSubmission(...args);
        entered.resolve(); await release.promise; return result;
      };
      let pending!: Promise<unknown>, sends = 0;
      await act(async () => {
        pending = sendPersistedComposer("race-tab", "old", "old", "navigation-race-id", async () => { sends++; });
        await entered.promise;
      });
      await paint("other-session");
      await paint("navigation-race");
      await act(async () => {
        release.resolve();
        await assert.rejects(pending, /inbox_not_submitted/);
      });
      assert.equal(sends, 0);
      assert.equal(editor.target!.initial.text, "do not send after leaving");
      host.commands.BeginSessionComposerSubmission = backend.BeginSessionComposerSubmission;
    });

    await t.test("rejected registration automatically recovers without replacing current input", async () => {
      const ref: SessionRef = { hostId: "local", sessionId: "registration-conflict" };
      await paint(ref.sessionId);
      await act(async () => patch("keep this window input"));
      host.commands.BeginSessionComposerSubmission = async () => {
        const current = await backend.GetSessionComposerState(ref);
        await backend.SaveSessionComposerState({ ref, expectedRevision: current.revision, contentVersion: 1, contentJson: '{"text":"other window input"}' });
        throw new Error("session_operation:input_conflict:The input changed. Both versions are preserved; review before continuing.");
      };
      let sends = 0;
      try {
        await act(async () => {
          await assert.rejects(sendPersistedComposer("race-tab", "keep this window input", "keep this window input", "conflicted-guidance", async () => { sends++; }, undefined, undefined, "guidance"), /input_conflict/);
        });
        assert.equal(editor.blocked, false, "automatic recovery must restore editability");
        assert.equal(editor.target!.initial.text, "keep this window input");
        await act(async () => editor.retry());
        assert.equal(JSON.parse((await backend.GetSessionComposerState(ref)).contentJson).text, "keep this window input");
        assert.equal(sends, 0, "conflict recovery never submits or replays the message");
      } finally {
        host.commands.BeginSessionComposerSubmission = backend.BeginSessionComposerSubmission;
      }
    });

    await t.test("lost registration response releases only the registration that was never sent", async () => {
      const ref: SessionRef = { hostId: "local", sessionId: "lost-registration" };
      await paint(ref.sessionId);
      await act(async () => { editor.setGoalDraft(true); patch("retained goal input"); });
      host.commands.BeginSessionComposerSubmission = async (...args: Parameters<typeof backend.BeginSessionComposerSubmission>) => {
        await backend.BeginSessionComposerSubmission(...args);
        throw new Error("lost registration response");
      };
      let sends = 0;
      await act(async () => {
        await assert.rejects(sendPersistedComposer("race-tab", "goal", "goal", "lost-registration-id", async () => { sends++; }), /lost registration/);
      });
      assert.equal(sends, 0);
      assert.equal(editor.blocked, false);
      assert.equal(editor.target!.initial.text, "retained goal input");
      assert.equal(editor.target!.initial.goalDraft, true);
      assert.equal(sends, 0);
      await act(async () => editor.retry());
      assert.equal(editor.blocked, false);
      assert.equal((await backend.GetSessionComposerState(ref)).submissionId, undefined);
      assert.equal(editor.target!.initial.goalDraft, true);
      host.commands.BeginSessionComposerSubmission = backend.BeginSessionComposerSubmission;
    });

    await t.test("unknown delivery is automatically reconciled without a second send", async () => {
      const ref: SessionRef = { hostId: "local", sessionId: "automatic-receipt" };
      await paint(ref.sessionId);
      await act(async () => patch("accepted once"));
      let sends = 0;
      host.commands.GetSessionComposerState = async value => {
        const state = await backend.GetSessionComposerState(value);
        if (value.sessionId === ref.sessionId && state.submissionId) return backend.CompleteSessionComposerSubmission(value, state.submissionId, "accepted");
        return state;
      };
      try {
        await act(async () => {
          await assert.rejects(sendPersistedComposer("race-tab", "accepted once", "accepted once", "automatic-receipt-id", async () => { sends++; throw Error("reply lost"); }), /reply lost/);
        });
        assert.equal(sends, 1);
        assert.equal(editor.blocked, false);
        assert.equal(editor.target!.initial.text, "");
        assert.equal(editor.settledId, "automatic-receipt-id");
      } finally { host.commands.GetSessionComposerState = backend.GetSessionComposerState; }
    });

    await t.test("history changes retain input and are acknowledged on explicit send", async () => {
      const ref: SessionRef = { hostId: "local", sessionId: "history-recovery" };
      await backend.SaveSessionComposerState({ ref, expectedRevision: "0", contentVersion: 1, contentJson: '{"text":"next instruction"}' });
      host.commands.GetSessionComposerState = async value => ({ ...await backend.GetSessionComposerState(value), historyChanged: value.sessionId === ref.sessionId });
      let acknowledged = false, sends = 0;
      host.commands.SaveSessionComposerState = async req => {
        if (req.ref.sessionId === ref.sessionId) acknowledged = req.acknowledgeHistory === true;
        return backend.SaveSessionComposerState(req);
      };
      try {
        await paint(ref.sessionId);
        assert.equal(editor.blocked, false);
        assert.equal(editor.target!.initial.text, "next instruction");
        await act(async () => { await sendPersistedComposer("race-tab", "next instruction", "next instruction", "history-recovery-id", async () => { sends++; }); });
        assert.equal(acknowledged, true);
        assert.equal(sends, 1);
      } finally {
        host.commands.GetSessionComposerState = backend.GetSessionComposerState;
        host.commands.SaveSessionComposerState = backend.SaveSessionComposerState;
      }
    });

    await t.test("another pending submission cannot consume this window's unsent input", async () => {
      const ref: SessionRef = { hostId: "local", sessionId: "other-pending" };
      await paint(ref.sessionId);
      await act(async () => patch("my unsent input"));
      const saved = await backend.SaveSessionComposerState({ ref, expectedRevision: "0", contentVersion: 1, contentJson: '{"text":"other instruction"}' });
      await backend.BeginSessionComposerSubmission(ref, saved.revision, "other-submission", "{}");
      let sends = 0;
      await act(async () => {
        await assert.rejects(sendPersistedComposer("race-tab", "my unsent input", "my unsent input", "unsent-id", async () => { sends++; }));
      });
      assert.equal(editor.blocked, true);
      assert.equal(editor.target!.initial.text, "my unsent input");
      await backend.CompleteSessionComposerSubmission(ref, "other-submission", "accepted");
      await act(async () => editor.retry());
      assert.equal(editor.blocked, false);
      assert.equal(editor.target!.initial.text, "my unsent input");
      assert.equal(sends, 0);
    });

    await t.test("lost settlement response follows durable content instead of reviving input", async () => {
      await paint("lost-settlement");
      await act(async () => patch("already received input"));
      host.commands.CompleteSessionComposerSubmission = async (...args: Parameters<typeof backend.CompleteSessionComposerSubmission>) => {
        const result = await backend.CompleteSessionComposerSubmission(...args);
        if (args[2] === "accepted") throw new Error("lost settlement response");
        return result;
      };
      let sends = 0;
      await act(async () => {
        await assert.rejects(sendPersistedComposer("race-tab", "received", "received", "lost-settlement-id", async () => { sends++; }), /lost settlement/);
      });
      assert.equal(sends, 1);
      assert.equal(editor.blocked, false);
      assert.equal(editor.target!.initial.text, "");
      host.commands.CompleteSessionComposerSubmission = backend.CompleteSessionComposerSubmission;
    });

    await t.test("a stale settlement snapshot cannot overwrite a newer recovered revision", async () => {
      const ref: SessionRef = { hostId: "local", sessionId: "stale-settlement" };
      await paint(ref.sessionId);
      await act(async () => patch("first input"));
      const settled = deferred(), response = deferred();
      host.commands.CompleteSessionComposerSubmission = async (...args: Parameters<typeof backend.CompleteSessionComposerSubmission>) => {
        const result = await backend.CompleteSessionComposerSubmission(...args);
        settled.resolve(); await response.promise; return result;
      };
      let pending!: Promise<unknown>;
      await act(async () => {
        pending = sendPersistedComposer("race-tab", "first", "first", "stale-settlement-id", async () => {});
        await settled.promise;
      });
      const current = await backend.GetSessionComposerState(ref);
      await backend.SaveSessionComposerState({ ref, expectedRevision: current.revision, contentVersion: 1, contentJson: '{"text":"another window input"}' });
      await act(async () => editor.retry());
      await act(async () => { response.resolve(); await pending; });
      assert.equal(editor.target!.initial.text, "another window input");
      host.commands.CompleteSessionComposerSubmission = backend.CompleteSessionComposerSubmission;
    });
  } finally {
    await act(async () => root.unmount());
    dom.window.close();
  }
});
