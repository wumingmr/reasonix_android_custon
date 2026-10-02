import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useSessionDraftSurface } from "../app-runtime/useSessionDraftSurface";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<body></body>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true,
  requestAnimationFrame: (fn: FrameRequestCallback) => setTimeout(() => fn(0), 0), cancelAnimationFrame: clearTimeout });
dom.window.requestAnimationFrame = globalThis.requestAnimationFrame;
dom.window.cancelAnimationFrame = globalThis.cancelAnimationFrame;
function deferred<T>() { let resolve!: (v: T) => void; const promise = new Promise<T>(r => { resolve = r; }); return { promise, resolve }; }
const content = (text: string) => ({ text, invocations: [], attachments: [], workspaceRefs: [], pastedBlocks: [], openPastedLabels: [], sessionRefs: [], selectedTextRefs: [] });
const settings = { model: "fixture/old", effort: "high", mode: "normal", collaborationMode: "normal", toolApprovalMode: "ask", disabledMcp: {}, mcpOrder: [] };
async function fixture(overrides: Record<string, unknown> = {}) {
  const records = new Map(["a", "b"].map(id => [id, { id, workspaceId: `ws-${id}`, scope: "project", workspaceRoot: `/tmp/${id}`, revision: 1, contentJson: JSON.stringify(content(`text-${id}`)), settings: structuredClone(settings), status: "active", updatedAt: 1 }]));
  const deleted: string[] = [];
  const stub = installDesktopHostStub({
    ListSessionDraftSummaries: async () => [],
    OpenSessionDraftForTarget: async (_: string, path: string) => structuredClone(records.get(path.split("/").pop()!)),
    GetDraftContext: async (id: string) => ({ draft: structuredClone(records.get(id)), commands: [], servers: [] }),
    GetSessionDraft: async (id: string) => structuredClone(records.get(id)),
    SetSessionDraftRestoreTarget: async () => {}, DismissSessionDraft: async () => {},
    SaveSessionDraft: async (req: { draftId: string; revision: number; contentJson: string; settings: typeof settings }) => {
      const old = records.get(req.draftId)!;
      if (req.revision !== old.revision) return { draft: structuredClone(old), conflict: true, outcome: "conflict" };
      const saved = { ...old, contentJson: req.contentJson, settings: req.settings, revision: old.revision + 1 };
      records.set(req.draftId, saved);
      return { draft: structuredClone(saved), conflict: false, outcome: "saved" };
    },
    DiscardSessionDraft: async (id: string) => { deleted.push(id); },
    ...overrides,
  });
  let owner!: ReturnType<typeof useSessionDraftSurface>;
  function Probe() { owner = useSessionDraftSurface({ onAccepted: () => {}, onChanged: () => {} }); return null; }
  const node = document.createElement("div"); document.body.append(node);
  const root = createRoot(node);
  await act(async () => { root.render(<Probe />); });
  await act(async () => { await owner.open("project", "/tmp/a"); });
  return { get owner() { return owner; }, records, deleted,
    close: async () => { await act(async () => { root.unmount(); }); stub.uninstall(); node.remove(); } };
}

try {
  for (const field of ["attachments", "workspaceRefs", "invocations", "pastedBlocks", "sessionRefs", "selectedTextRefs"]) {
    let confirmations = 0;
    const f = await fixture({ ConfirmAction: async () => { confirmations++; return false; } });
    const payload = JSON.parse(JSON.stringify(content("")));
    payload[field] = [{ id: "saved-content", name: "keep", path: "/tmp/keep", text: "keep" }];
    act(() => f.owner.updateContent(payload));
    await act(async () => { await f.owner.confirmDiscard({ title: "Discard", message: "Discard", detail: "Cannot restore", confirmLabel: "Discard", cancelLabel: "Cancel" }); });
    assert.equal(confirmations, 1, `${field} without body text requires confirmation`);
    assert.deepEqual(f.deleted, [], `${field} survives cancellation`);
    await f.close();
  }
  {
    let confirmations = 0;
    const f = await fixture({ ConfirmAction: async () => { confirmations++; return false; } });
    const labels = { title: "Discard", message: "Discard", detail: "Cannot restore", confirmLabel: "Discard", cancelLabel: "Cancel" };
    await act(async () => { await f.owner.confirmDiscard(labels); });
    assert.deepEqual(f.deleted, [], "cancel keeps the saved draft");
    assert.equal(f.owner.surface!.content.text, "text-a");
    act(() => f.owner.updateContent(content("")));
    await act(async () => { await f.owner.confirmDiscard(labels); });
    assert.equal(confirmations, 1, "an empty draft needs no confirmation");
    assert.deepEqual(f.deleted, ["a"]);
    assert.equal(f.owner.surface, null);
    await f.close();
  }
  {
    const f = await fixture({ DiscardSessionDraft: async () => { throw new Error("write failed"); } });
    await act(async () => { await assert.rejects(f.owner.discard(), /write failed/); });
    assert.equal(f.owner.surface!.content.text, "text-a", "failed discard retains content");
    assert.equal(f.owner.surface!.discarding, false, "failed discard unlocks retry");
    await f.close();
  }
  {
    const gate = deferred<void>();
    let calls = 0;
    const f = await fixture({ DiscardSessionDraft: () => { calls++; return gate.promise; } });
    let first!: Promise<void>;
    act(() => { first = f.owner.discard(); });
    await act(async () => { await f.owner.discard(); });
    assert.equal(calls, 1, "duplicate clicks share the in-flight deletion");
    assert.equal(f.owner.surface!.discarding, true);
    await act(async () => { gate.resolve(); await first; });
    await f.close();
  }
  {
    const confirmation = deferred<boolean>();
    const f = await fixture({ ConfirmAction: () => confirmation.promise });
    let pending!: Promise<void>;
    act(() => { pending = f.owner.confirmDiscard({ title: "", message: "", detail: "", confirmLabel: "", cancelLabel: "" }); });
    act(() => f.owner.updateContent(content("new edit after confirmation opened")));
    await act(async () => { confirmation.resolve(true); await assert.rejects(pending, /draft changed/); });
    assert.deepEqual(f.deleted, [], "confirmation cannot discard unseen edits");
    assert.equal(f.owner.surface!.content.text, "new edit after confirmation opened");
    await f.close();
  }
  {
    const f = await fixture();
    act(() => { f.owner.captureSubmission("a", 1); });
    await act(async () => { await assert.rejects(f.owner.discard(), /Cancel the submission/); });
    assert.deepEqual(f.deleted, [], "submission owns the draft until cancellation is acknowledged");
    await f.close();
  }
  {
    const f = await fixture();
    await act(async () => { await f.owner.open("project", "/tmp/b"); });
    act(() => { f.owner.reportTaskError("a", 1, "attachment failed in A"); });
    assert.equal(f.owner.surface!.taskError, undefined, "background errors do not appear on B");
    await act(async () => { await f.owner.open("project", "/tmp/a"); });
    assert.equal(f.owner.surface!.taskError, "attachment failed in A");
    act(() => { f.owner.reportTaskError("a", 0, "stale error"); });
    assert.equal(f.owner.surface!.taskError, "attachment failed in A", "old generations cannot replace task errors");
    await f.close();
  }
  {
    const requests: { requestId: string }[] = [];
    const f = await fixture({
      GetSessionDraftState: async () => ({ operation: undefined }),
      BeginDraftSubmission: async (request: { requestId: string }) => {
        requests.push(request);
        if (requests.length === 1) throw new Error("connection lost before the first transaction completed");
        return { operationId: "same-op", requestId: request.requestId, draftId: "a", phase: "accepted", revision: 2, submissionId: "same-submit", updatedAt: 1, session: { hostId: "local", sessionId: "same-session" } };
      },
    });
    let capture: ReturnType<typeof f.owner.captureSubmission>;
    act(() => { capture = f.owner.captureSubmission("a", 1); });
    await act(async () => { await f.owner.submit("text-a", "text-a", undefined, undefined, capture); });
    assert.equal(requests.length, 2);
    assert.equal(requests[0].requestId, requests[1].requestId, "absence during a lost Begin retries the original preparation identity");
    assert.equal(f.owner.surface, null);
    await f.close();
  }
  {
    const operation = { operationId: "original-op", draftId: "a", phase: "resume_required", revision: 7, submissionId: "original-submit", canResume: true, canEdit: false, canCancel: true, canDiscard: false, updatedAt: 1, session: { hostId: "local", sessionId: "original-session" } };
    const calls: unknown[] = [];
    const f = await fixture({
      GetDraftContext: async () => ({ draft: { id: "a", workspaceId: "ws-a", scope: "project", workspaceRoot: "/tmp/a", revision: 1, contentJson: JSON.stringify(content("text-a")), settings, status: "active", updatedAt: 1 }, operation, commands: [], servers: [] }),
      ResumeDraftSubmission: async (...args: unknown[]) => { calls.push(args); return { ...operation, phase: "accepted", revision: 8 }; },
    });
    assert.equal(f.owner.surface!.operation!.operationId, "original-op");
    assert.equal(f.owner.captureSubmission("a", 1), null, "restored operation cannot accidentally prepare a new submission");
    await act(async () => { await f.owner.resumeSubmission(); });
    assert.deepEqual(calls, [["original-op", 7]], "explicit continuation uses the original operation and expected version");
    await f.close();
  }
  {
    const operation = { operationId: "failed-runtime", draftId: "a", phase: "runtime_failed", revision: 7, submissionId: "original-submit", canResume: true, canEdit: false, canCancel: true, canDiscard: false, updatedAt: 1 };
    const calls: unknown[] = [];
    const retry = deferred<typeof operation>();
    const f = await fixture({
      GetDraftContext: async () => ({ draft: { id: "a", workspaceId: "ws-a", scope: "project", workspaceRoot: "/tmp/a", revision: 1, contentJson: JSON.stringify(content("keep my draft")), settings, status: "active", updatedAt: 1 }, operation, commands: [], servers: [] }),
      ResumeDraftSubmission: async (...args: unknown[]) => {
        calls.push(args);
        if (calls.length === 1) throw new Error("create session runtime: path unavailable");
        return retry.promise;
      },
      BeginDraftSubmission: async () => { throw new Error("retry must reuse the original operation"); },
    });
    await act(async () => { await f.owner.resumeSubmission(); });
    assert.equal(f.owner.surface!.saveState, "saved", "runtime failure is not a persistence failure");
    assert.equal(f.owner.surface!.error, undefined);
    assert.equal(f.owner.surface!.submissionError, "create session runtime: path unavailable");
    assert.equal(f.owner.surface!.content.text, "keep my draft");
    let pending!: Promise<void>;
    await act(async () => {
      pending = f.owner.resumeSubmission();
      await f.owner.resumeSubmission();
    });
    assert.equal(f.owner.surface!.resumingSubmission, true);
    assert.deepEqual(calls, [["failed-runtime", 7], ["failed-runtime", 7]], "overlapping retries share one in-flight resume");
    await act(async () => { retry.resolve({ ...operation, phase: "runtime_failed", revision: 8 }); await pending; });
    assert.equal(f.owner.surface!.submissionError, undefined, "successful resume RPC clears its previous error");
    assert.equal(f.owner.surface!.resumingSubmission, false);
    assert.equal(f.owner.surface!.content.text, "keep my draft");
    await f.close();
  }
  {
    const timers: (() => void)[] = [];
    const originalTimer = window.setTimeout;
    window.setTimeout = ((callback: () => void, delay?: number) => delay && delay >= 250 ? (timers.push(callback), timers.length) : originalTimer(callback, delay)) as typeof window.setTimeout;
    const operation = { operationId: "unknown-op", draftId: "a", phase: "dispatching_shell", revision: 4, submissionId: "shell-submit", canResume: false, canEdit: false, canCancel: true, canDiscard: false, updatedAt: 1, session: { hostId: "local", sessionId: "shell-session" } };
    const f = await fixture({
      GetDraftContext: async () => ({ draft: { id: "a", workspaceId: "ws-a", scope: "project", workspaceRoot: "/tmp/a", revision: 1, contentJson: JSON.stringify(content("text-a")), settings, status: "active", updatedAt: 1 }, operation, commands: [], servers: [] }),
      CancelDraftSubmission: async () => ({ ...operation, phase: "accepted", revision: 6 }),
      GetDraftSubmission: async () => ({ ...operation, revision: 5 }),
    });
    assert.ok(timers.length, "shell dispatch keeps an application-level verification loop");
    await act(async () => { await f.owner.cancelSubmission(); });
    assert.equal(f.owner.surface, null, "accepted cancellation converts immediately without waiting for the poll timer");
    await act(async () => { timers.shift()?.(); });
    assert.equal(f.owner.surface, null, "an older poll cannot resurrect the accepted draft");
    await f.close();
    window.setTimeout = originalTimer;
  }
  {
    const f = await fixture();
    act(() => { f.owner.captureSubmission("a", 1); });
    act(() => { f.owner.updateSettings({ model: "fixture/new" }); f.owner.updateContent(content("new text")); });
    assert.equal(f.owner.surface!.preparingSubmission, true, "capture synchronously locks the owner before context expansion");
    assert.equal(f.owner.surface!.settings.model, "fixture/old");
    assert.equal(f.owner.surface!.content.text, "text-a");
    await f.close();
  }
  {
    const confirmation = deferred<boolean>();
    const started = deferred<void>();
    const f = await fixture({ ConfirmAction: () => { started.resolve(); return confirmation.promise; } });
    let pending!: Promise<void>;
    act(() => { pending = f.owner.confirmDiscard({ title: "", message: "", detail: "", confirmLabel: "", cancelLabel: "" }); });
    await started.promise;
    await act(async () => { await f.owner.open("project", "/tmp/b"); });
    await act(async () => { confirmation.resolve(true); await pending; });
    assert.deepEqual(f.deleted, ["a"], "confirmation remains bound to A");
    assert.equal(f.owner.surface!.draft.id, "b");
    await f.close();
  }
  {
    const finished = deferred<void>();
    const f = await fixture({ DiscardSessionDraft: () => finished.promise });
    let pending!: Promise<void>;
    act(() => { pending = f.owner.discard(); });
    await act(async () => { await f.owner.open("project", "/tmp/b"); });
    await act(async () => { finished.resolve(); await pending; });
    assert.equal(f.owner.surface!.draft.id, "b", "late discard cannot dismiss B");
    await f.close();
  }
  {
    const task = deferred<void>();
    const f = await fixture();
    let tracked!: Promise<void>;
    act(() => { tracked = f.owner.trackTask("a", 1, task.promise); f.owner.updateContent(content("local")); });
    const saved = f.records.get("a")!;
    f.records.set("a", { ...saved, revision: 2, contentJson: JSON.stringify(content("remote")) });
    await act(async () => { await f.owner.flush(); });
    assert.equal(f.owner.surface!.saveState, "conflict");
    act(() => { f.owner.useSavedConflict(); });
    await act(async () => { task.resolve(); await tracked; });
    assert.equal(f.owner.surface!.pendingTasks, 0, "old generation still releases its task registration");
    await act(async () => { await window.__reasonixFlushSessionDraft!(); });
    await f.close();
  }
  console.log("draft ownership: synchronous capture, target-bound discard and task settlement passed");
} finally { dom.window.close(); }
