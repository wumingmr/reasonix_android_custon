import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { installDesktopHostStub } from "./desktopHostStub";
import { createManualSession } from "../lib/manualCreationRequests";
import type { ManualSessionCreationRequest, ManualSessionCreationView } from "../generated/desktopContract.generated";

const dom = new JSDOM("", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document });
const request: ManualSessionCreationRequest = { operationId: "lost-response", workspaceId: "", scope: "project", workspaceRoot: "/workspace" };
const operation: ManualSessionCreationView = { ...request, workspaceId: "canonical", scope: "project", workspaceRoot: "/workspace",
  ref: { hostId: "local", sessionId: "same-session" }, topicId: "same-topic", phase: "ready",
  settings: { model: "stub", mode: "normal", toolApprovalMode: "default", disabledMcp: {}, mcpOrder: [] } };
const begins: string[] = [];
const reads: string[] = [];
const opened: string[] = [];
const stub = installDesktopHostStub({
  BeginManualSessionCreation: async (input: ManualSessionCreationRequest) => {
    begins.push(input.operationId);
    throw new Error("connection closed after persistence");
  },
  GetManualSessionCreation: async (id: string) => { reads.push(id); return operation; },
});
try {
  const result = await createManualSession(request, { onSurfaceReady: async view => { opened.push(view.ref.sessionId); } });
  assert.equal(result.ref.sessionId, "same-session");
  assert.deepEqual(begins, [request.operationId], "lost acknowledgement never allocates another creation request");
  assert.deepEqual(reads, [request.operationId], "recovery reads the durable original request");
  assert.deepEqual(opened, ["same-session"], "older hosts without surfaceReady still open at ready");
  stub.commands.BeginManualSessionCreation = async () => ({ ...operation, phase: "starting", progress: {
    status: "blocked", stage: "preparing_storage", errorCode: "unsupported_ui_schema", stageStartedAt: 1, elapsedMs: 0, slow: false,
  } });
  const blocked = await createManualSession(request);
  assert.equal(blocked.progress?.status, "blocked", "terminal host progress ends observation even if the last durable phase was starting");
  assert.equal(reads.length, 1, "a blocked operation does not poll indefinitely");
  for (const status of ["queued", "running", "waiting_lock", "waiting_workspace", "retrying_storage"]) {
    stub.commands.BeginManualSessionCreation = async () => ({ ...operation, phase: "failed", error: "session_operation:target_changed:previous failure", progress: {
      status, stage: "queued", stageStartedAt: 1, elapsedMs: 0, slow: false,
    } });
    const recovered = await createManualSession(request);
    assert.equal(recovered.phase, "ready", `${status} observes automatic recovery despite the older durable failed phase`);
  }
  assert.equal(reads.length, 6, "all active recovery statuses read the eventual result instead of reporting stale failure");
  stub.commands.BeginManualSessionCreation = async (input: ManualSessionCreationRequest) => {
    begins.push(input.operationId);
    return { ...operation, phase: "starting", surfaceReady: true };
  };
  const abandoned = await createManualSession(request, {
    isObservationCurrent: () => false,
    onSurfaceReady: async view => { opened.push(view.ref.sessionId); },
  });
  assert.equal(abandoned.phase, "starting", "stale observation leaves the acknowledged host operation running");
  assert.equal(begins.length, 2, "the requested creation is accepted even when navigation becomes stale");
  assert.equal(reads.length, 6, "a stale observer does not start a poll");
  assert.equal(opened.length, 1, "a stale observer never opens its target");
  let observing = true;
  await createManualSession(request, {
    isObservationCurrent: () => observing,
    onSurfaceReady: async () => { observing = false; },
  });
  assert.equal(reads.length, 6, "leaving during surface activation stops observation without cancelling creation");
  const retried: string[] = [];
  const updates: string[] = [];
  stub.commands.BeginManualSessionCreation = async () => ({ ...operation, phase: "failed" });
  stub.commands.RetryManualSessionCreation = async (id: string) => {
    retried.push(id);
    return { ...operation, phase: "starting", surfaceReady: true, progress: { status: "running", stage: "building_runtime", elapsedMs: 1, stageStartedAt: 1, slow: false } };
  };
  let poll = 0;
  stub.commands.GetManualSessionCreation = async () => ++poll === 1
    ? { ...operation, phase: "starting", surfaceReady: true, progress: { status: "running", stage: "building_runtime", elapsedMs: 500, stageStartedAt: 1, slow: false } }
    : { ...operation, surfaceReady: true };
  await createManualSession(request, { retry: true, onSurfaceReady: async () => {}, onProgress: view => { updates.push(view.phase); } });
  assert.deepEqual(retried, [request.operationId], "retry keeps the original durable request identity");
  assert.deepEqual(updates, ["starting", "ready"], "elapsed time alone does not repaint the app or duplicate notices");
  console.log("PASS manual creation recovers lost acknowledgements with the original identity and older-host fallback");
} finally {
  stub.uninstall();
  dom.window.close();
}
