import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { flushAllSessionComposers, resumeSessionComposerEditing, useSessionComposerPersistence } from "../lib/sessionComposerPersistence";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { installDesktopHostStub } from "./desktopHostStub";

const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(done => { resolve = done; }); return { promise, resolve }; };
const tick = () => new Promise<void>(done => setImmediate(done));

test("accepted input survives autosave recovery and close", async t => {
  const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const backend = makeSessionUIMock(async () => {});
  const host = installDesktopHostStub({ ...backend });
  const root = createRoot(document.getElementById("root")!);
  let editor!: ReturnType<typeof useSessionComposerPersistence>;
  const ref = (sessionId: string) => ({ hostId: "local", sessionId });
  function Probe({ session }: { session: string }) { editor = useSessionComposerPersistence(ref(session), session); return null; }
  const paint = async (session: string) => { await act(async () => root.render(<Probe session={session} />)); };
  try {
    for (const kind of ["attachment", "clipboard", "workspace", "failed-read"] as const) await t.test(kind, async () => {
      await paint(kind);
      const owner = editor.target!;
      const releaseTask = deferred(), enteredRead = deferred(), releaseRead = deferred();
      let writes = 0, readFails = kind === "failed-read", saveFails = false;
      host.commands.SaveSessionComposerState = async (request: Parameters<typeof backend.SaveSessionComposerState>[0]) => {
        if (++writes === 1 || saveFails) throw Error("storage unavailable");
        return backend.SaveSessionComposerState(request);
      };
      host.commands.GetSessionComposerState = async (value: Parameters<typeof backend.GetSessionComposerState>[0]) => {
        if (value.sessionId === kind) { enteredRead.resolve(); await releaseRead.promise; }
        if (readFails) throw Error("read unavailable");
        return backend.GetSessionComposerState(value);
      };
      t.mock.timers.enable({ apis: ["setTimeout"] });
      try {
        await act(async () => owner.onPatch!(owner.draftId, owner.generation, { text: "retained text" }));
        const task = owner.trackTask!(owner.draftId, owner.generation, releaseTask.promise.then(() => {
          owner.onPatch!(owner.draftId, owner.generation, current => ({ ...current,
            ...(kind === "attachment" ? { attachments: [...current.attachments, { path: ".reasonix/attachments/accepted.png" }] }
              : kind === "workspace" ? { workspaceRefs: [...current.workspaceRefs, { path: "src/accepted.ts" }] }
              : { text: current.text + " pasted result", pastedBlocks: [{ label: "pasted result", text: "full pasted content" }] }),
          }));
        }));
        await act(async () => { t.mock.timers.tick(250); await enteredRead.promise; });
        assert.equal(editor.recovering, true);
        assert.equal(owner.canEdit!(owner.draftId, owner.generation), false, "new input remains locked");
        let closing: Promise<void> | undefined, closed = false;
        if (!readFails) await act(async () => {
          closing = flushAllSessionComposers().then(() => { closed = true; });
          await tick();
        });
        if (readFails) await act(async () => { releaseRead.resolve(); await tick(); });
        await act(async () => { releaseTask.resolve(); await task; });
        assert.equal(closed, false, "close must still wait for recovery before saving accepted input");
        // Accepted publication must not clear an unreadable-store failure.
        if (readFails) {
          assert.match(editor.error!, /read unavailable/);
          await act(async () => { await assert.rejects(flushAllSessionComposers(), /read unavailable/); });
          readFails = false;
        }
        await act(async () => { closing ??= flushAllSessionComposers(); releaseRead.resolve(); await closing; });
        const durable = JSON.parse((await backend.GetSessionComposerState(ref(kind))).contentJson);
        if (kind === "attachment") assert.deepEqual(durable.attachments, [{ path: ".reasonix/attachments/accepted.png" }]);
        else if (kind === "workspace") assert.deepEqual(durable.workspaceRefs, [{ path: "src/accepted.ts" }]);
        else {
          assert.equal(durable.text, "retained text pasted result");
          assert.deepEqual(durable.pastedBlocks, [{ label: "pasted result", text: "full pasted content" }]);
        }
        // A later real save failure still vetoes close and retains local input.
        await act(async () => {
          resumeSessionComposerEditing(); saveFails = true;
          owner.onPatch!(owner.draftId, owner.generation, { text: "genuinely unsaved input" });
          await assert.rejects(flushAllSessionComposers(), /storage unavailable/);
        });
        assert.equal(editor.target!.initial.text, "genuinely unsaved input");
        assert.equal((await backend.GetSessionComposerState(ref(kind))).contentJson, JSON.stringify(durable));
      } finally {
        releaseTask.resolve(); releaseRead.resolve(); readFails = false; saveFails = false;
        host.commands.GetSessionComposerState = backend.GetSessionComposerState;
        host.commands.SaveSessionComposerState = backend.SaveSessionComposerState;
        await act(async () => { await editor.retry(); resumeSessionComposerEditing(); });
        t.mock.timers.reset();
      }
    });
  } finally {
    await act(async () => { resumeSessionComposerEditing(); root.unmount(); });
    host.uninstall(); dom.window.close();
  }
});
