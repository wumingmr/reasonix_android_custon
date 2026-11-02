import assert from "node:assert/strict";
import test from "node:test";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { app } from "../lib/bridge";
import type { SessionLifecycleRequest } from "../generated/desktopContract.generated";
import { flushAllSessionComposers, resumeSessionComposerEditing, useSessionComposerPersistence } from "../lib/sessionComposerPersistence";
import { makeSessionUIMock } from "../lib/sessionUIMock";
import { installDesktopHostStub } from "./desktopHostStub";

const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(done => { resolve = done; }); return { promise, resolve }; };
const tick = () => new Promise<void>(done => setImmediate(done));
const ref = (sessionId: string) => ({ hostId: "local", sessionId });

test("only confirmed permanent deletion retires a composer owner", async t => {
  const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const backend = makeSessionUIMock(async () => {});
  const deleted = new Set<string>(), failedSaves = new Set<string>(), rejectedPurges = new Set<string>();
  let writes = 0;
  const host = installDesktopHostStub({ ...backend,
    GetSessionComposerState: async (value: Parameters<typeof backend.GetSessionComposerState>[0]) => {
      if (deleted.has(value.sessionId)) throw Error("session not found");
      return backend.GetSessionComposerState(value);
    },
    SaveSessionComposerState: async (request: Parameters<typeof backend.SaveSessionComposerState>[0]) => {
      writes++;
      if (deleted.has(request.ref.sessionId)) throw Error("session not found");
      if (failedSaves.has(request.ref.sessionId)) throw Error("storage unavailable");
      return backend.SaveSessionComposerState(request);
    },
    ApplySessionLifecycle: async (request: SessionLifecycleRequest) => ({
      operationId: request.operationId, generation: 1, committed: false,
      items: request.targets.map(target => {
        const committed = !rejectedPurges.has(target.ref!.sessionId);
        if (committed && request.action === "purge") deleted.add(target.ref!.sessionId);
        return { target, ref: target.ref, workspaceId: "global", committed, retryable: !committed };
      }),
    }),
    PurgeCanonicalSession: async (value: ReturnType<typeof ref>) => {
      if (rejectedPurges.has(value.sessionId)) throw Error("purge failed");
      deleted.add(value.sessionId);
    },
  });
  const root = createRoot(document.getElementById("root")!);
  let editor!: ReturnType<typeof useSessionComposerPersistence>;
  function Probe({ session, tab = session }: { session: string; tab?: string }) { editor = useSessionComposerPersistence(ref(session), tab); return null; }
  const paint = async (session: string, tab?: string) => { await act(async () => root.render(<Probe session={session} tab={tab} />)); };
  const lifecycle = (action: string, ...ids: string[]) => app.ApplySessionLifecycle({ action, operationId: `${action}-${ids.join("-")}`, expectedGeneration: 0, targets: ids.map(id => ({ ref: ref(id) })) });
  const dirty = async (id: string) => {
    await paint(id);
    failedSaves.add(id);
    const owner = editor.target!;
    await act(async () => {
      owner.onPatch!(owner.draftId, owner.generation, { text: `unsaved ${id}` });
      await assert.rejects(flushAllSessionComposers(), /storage unavailable/);
    });
    return owner;
  };
  try {
    await t.test("bulk purge retires committed items only; archive and missing-session errors preserve protection", async () => {
      const a = await dirty("bulk-a"), b = await dirty("bulk-b");
      await paint("blank");
      await act(async () => {
        await lifecycle("archive", "bulk-a", "bulk-b");
        await assert.rejects(flushAllSessionComposers(), /storage unavailable/);
      });
      assert.equal(a.isCurrent!(a.draftId, a.generation), true);
      rejectedPurges.add("bulk-b");
      await act(async () => {
        await lifecycle("purge", "bulk-a", "bulk-b");
        await assert.rejects(flushAllSessionComposers(), /storage unavailable/);
      });
      assert.equal(a.isCurrent!(a.draftId, a.generation), false);
      assert.equal(b.isCurrent!(b.draftId, b.generation), true);
      // A missing-session error without a successful purge receipt is unsafe
      // to treat as permission to discard the remaining draft.
      deleted.add("bulk-b");
      await act(async () => { await assert.rejects(flushAllSessionComposers(), /session not found/); });
      rejectedPurges.delete("bulk-b");
      await act(async () => { await lifecycle("purge", "bulk-b"); await flushAllSessionComposers(); });
      assert.equal(editor.target!.initial.text, "");
      await act(async () => { resumeSessionComposerEditing(); await flushAllSessionComposers(); resumeSessionComposerEditing(); });
    });

    await t.test("direct purge revokes late tasks and releases an in-progress close", async () => {
      const owner = await dirty("direct");
      const release = deferred();
      const task = owner.trackTask!(owner.draftId, owner.generation, release.promise.then(() => {
        owner.onPatch!(owner.draftId, owner.generation, { text: "late clipboard", attachments: [{ path: "late.png" }] });
        owner.onTaskError!(owner.draftId, owner.generation, "late attachment error");
      }));
      let closing!: Promise<void>;
      try {
        rejectedPurges.add("direct");
        await act(async () => { await assert.rejects(app.PurgeCanonicalSession(ref("direct")), /purge failed/); });
        assert.equal(owner.isCurrent!(owner.draftId, owner.generation), true);
        await act(async () => { closing = flushAllSessionComposers(); await tick(); });
        rejectedPurges.delete("direct");
        await act(async () => { await app.PurgeCanonicalSession(ref("direct")); await closing; });
        assert.equal(owner.isCurrent!(owner.draftId, owner.generation), false);
        assert.equal(owner.canEdit!(owner.draftId, owner.generation), false);
        const before = writes;
        await act(async () => { release.resolve(); await task; });
        await paint("direct", "rebound-tab");
        await act(async () => flushAllSessionComposers());
        assert.equal(writes, before, "late callbacks and effect rebinding cannot resurrect a purged owner");
        assert.equal(editor.taskError, undefined);
      } finally {
        release.resolve(); await task;
        await act(async () => resumeSessionComposerEditing());
      }
    });

    for (const phase of ["read", "save"] as const) await t.test(`purge fences an in-flight ${phase} response`, async () => {
      const id = `pending-${phase}`, entered = deferred(), release = deferred();
      if (phase === "save") await paint(id);
      const method = phase === "read" ? "GetSessionComposerState" : "SaveSessionComposerState";
      const previous = host.commands[method];
      host.commands[method] = async () => { entered.resolve(); await release.promise; throw Error("session not found"); };
      let closing!: Promise<void>;
      try {
        if (phase === "read") await paint(id);
        else await act(async () => {
          const owner = editor.target!;
          owner.onPatch!(owner.draftId, owner.generation, { text: "pending write" });
        });
        await act(async () => { closing = flushAllSessionComposers(); await entered.promise; });
        await act(async () => { await lifecycle("purge", id); await closing; });
        await act(async () => { release.resolve(); await tick(); });
        host.commands[method] = previous;
        await paint(`blank-after-${phase}`);
        await act(async () => flushAllSessionComposers());
      } finally {
        release.resolve(); host.commands[method] = previous;
        await act(async () => { await editor.retry(); resumeSessionComposerEditing(); });
      }
    });
  } finally {
    failedSaves.clear();
    await act(async () => { resumeSessionComposerEditing(); root.unmount(); });
    host.uninstall(); dom.window.close();
  }
});
