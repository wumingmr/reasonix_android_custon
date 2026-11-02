import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { app, sessionPurgeHandlers } from "../lib/bridge";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { installDesktopHostStub } from "./desktopHostStub";

test("purge stays lazy but retires an owner loaded before its receipt", async () => {
  const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost/" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const host = installDesktopHostStub({
    ...makeSessionUIMock(async () => {}),
    PurgeCanonicalSession: async () => {},
    SaveSessionComposerState: async () => { throw Error("storage unavailable"); },
  });
  const ref = { hostId: "local", sessionId: "lazy-purge" };
  const root = createRoot(document.getElementById("root")!);
  let finish!: () => void;
  try {
    assert.deepEqual(Object.keys(sessionPurgeHandlers), []);
    await app.PurgeCanonicalSession(ref);
    assert.deepEqual(Object.keys(sessionPurgeHandlers), [], "purge without an owner must not load draft persistence");

    const pending = new Promise<void>(resolve => { finish = resolve; });
    host.commands.PurgeCanonicalSession = () => pending;
    const purging = app.PurgeCanonicalSession(ref);
    const { useSessionComposerPersistence, flushAllSessionComposers, resumeSessionComposerEditing } = await import("../lib/sessionComposerPersistence");
    let editor!: ReturnType<typeof useSessionComposerPersistence>;
    function Probe() { editor = useSessionComposerPersistence(ref, ref.sessionId); return null; }
    await act(async () => { root.render(<Probe />); });
    const owner = editor.target!;
    await act(async () => { owner.onPatch!(owner.draftId, owner.generation, { text: "retire only after receipt" }); });
    assert.equal(owner.isCurrent!(owner.draftId, owner.generation), true);
    await act(async () => { finish(); await purging; });
    assert.equal(owner.isCurrent!(owner.draftId, owner.generation), false, "the completion observes the newly registered owner");
    await act(async () => { await flushAllSessionComposers(); resumeSessionComposerEditing(); });
  } finally {
    finish?.();
    await act(async () => root.unmount());
    host.uninstall();
    dom.window.close();
  }
});
