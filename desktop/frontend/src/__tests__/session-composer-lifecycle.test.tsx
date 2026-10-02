import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useSessionComposerLifecycle } from "../app-runtime/useSessionComposerLifecycle";
import { useSessionComposerPersistence } from "../lib/sessionComposerPersistence";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
const backend = makeSessionUIMock(async () => {});
let failSave = false;
const legacyCalls: string[] = [];
const retired = (name: string) => async () => { legacyCalls.push(name); throw Error("corrupt old draft database"); };
const stub = installDesktopHostStub({ ...backend,
  SaveSessionComposerState: async (request: Parameters<typeof backend.SaveSessionComposerState>[0]) => {
    if (failSave) throw Error("formal input save failed");
    return backend.SaveSessionComposerState(request);
  },
  ListSessionDraftSummaries: retired("list"), RestoreSessionDraft: retired("restore"), SaveSessionDraft: retired("save"),
});
const ref = { hostId: "local", sessionId: "formal-lifecycle" };
let editor!: ReturnType<typeof useSessionComposerPersistence>;
function Probe() {
  useSessionComposerLifecycle();
  editor = useSessionComposerPersistence(ref, ref.sessionId);
  return null;
}
const root = createRoot(document.getElementById("root")!);
const patch = (text: string) => {
  const target = editor.target!;
  target.onPatch!(target.draftId, target.generation, { text });
};
try {
  await act(async () => { root.render(<Probe />); });
  await act(async () => { patch("keep formal input"); });
  let release!: () => void;
  const pending = new Promise<void>(resolve => { release = resolve; });
  const target = editor.target!;
  target.trackTask!(target.draftId, target.generation, pending.then(() => patch("attachment finished")));
  let finished = false;
  let flushing!: Promise<void>;
  act(() => { flushing = window.__reasonixFlushSessionDraft!().then(() => { finished = true; }); });
  assert.equal(finished, false);
  await act(async () => { release(); await flushing; });
  assert.equal(JSON.parse((await backend.GetSessionComposerState(ref)).contentJson).text, "attachment finished");
  await act(async () => { window.__reasonixResumeSessionDraftEditing!(); });
  failSave = true;
  await act(async () => patch("keep on failure"));
  await act(async () => { await assert.rejects(window.__reasonixFlushSessionDraft!(), /formal input save failed/); });
  failSave = false;
  await act(async () => { await editor.retry(); });
  assert.equal(editor.blocked, false, "failed exit resumes formal editing");
  assert.deepEqual(legacyCalls, [], "startup and shutdown never inspect retired drafts");

  // The production mount must use this owner, otherwise isolated hook tests
  // would miss a reintroduced legacy startup/read/exit path.
  const runtime = readFileSync(new URL("../AppRuntime.tsx", import.meta.url), "utf8");
  assert.match(runtime, /useSessionComposerLifecycle\(\)/);
  assert.doesNotMatch(runtime, /useSessionDraftSurface|acceptedDraftSession/);
  const view = readFileSync(new URL("../app-shell/AppRuntimeView.tsx", import.meta.url), "utf8");
  assert.doesNotMatch(view, /draft\.surface|PreviousInputRecovery|DraftTopicbarActions/);
  await act(async () => root.unmount());
  assert.equal(window.__reasonixFlushSessionDraft, undefined);
  assert.equal(window.__reasonixResumeSessionDraftEditing, undefined);
  console.log("PASS formal input lifecycle: exit attachment barrier, save failure, unmount, no legacy reads");
} finally {
  stub.uninstall();
  dom.window.close();
}
