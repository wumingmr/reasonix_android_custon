import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { sendPersistedComposer, useSessionComposerPersistence } from "../lib/sessionComposerPersistence";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
const backend = makeSessionUIMock(async () => {});
const stub = installDesktopHostStub(backend);
let editor!: ReturnType<typeof useSessionComposerPersistence>;
const ref = { hostId: "local", sessionId: "formal-diagnostic" };
function Probe() { editor = useSessionComposerPersistence(ref, ref.sessionId); return null; }
const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => { root.render(<Probe />); });
  const target = editor.target!;
  await act(async () => { target.onPatch!(target.draftId, target.generation, { text: "你好" }); });
  let sends = 0;
  await act(async () => {
    // As in Composer.submit, track the submission itself for exit persistence.
    await target.trackTask!(target.draftId, target.generation,
      sendPersistedComposer(ref.sessionId, "你好", "你好", "formal-diagnostic-send", async () => { sends++; }));
  });
  assert.equal(sends, 1);
  assert.equal(editor.blocked, false);
  assert.equal(editor.error, undefined);
  assert.equal(JSON.parse((await backend.GetSessionComposerState(ref)).contentJson).text, undefined);
  console.log("PASS formal submission does not count itself as an unfinished attachment; send callback=1");
} finally {
  await act(async () => { root.unmount(); });
  stub.uninstall();
  dom.window.close();
}
