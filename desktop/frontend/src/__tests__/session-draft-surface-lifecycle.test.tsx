import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";

import { useSessionDraftSurface } from "../app-runtime/useSessionDraftSurface";
import type { PersistentComposerDraft } from "../components/Composer";
import type {
  SessionDraftSubmissionRequest,
  SessionDraftSubmissionView,
  SessionDraftView,
  ServerView,
} from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => { resolve = next; });
  return { promise, resolve };
}

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  IS_REACT_ACT_ENVIRONMENT: true,
  requestAnimationFrame: (callback: FrameRequestCallback) => setTimeout(() => callback(0), 0),
  cancelAnimationFrame: (id: number) => clearTimeout(id),
});
dom.window.requestAnimationFrame = globalThis.requestAnimationFrame;
dom.window.cancelAnimationFrame = globalThis.cancelAnimationFrame;

const settings = {
  model: "fixture-model",
  mode: "normal",
  toolApprovalMode: "ask",
  disabledMcp: {},
  mcpOrder: [],
};
const drafts = new Map<string, SessionDraftView>();
for (const [id, root] of [["draft-a", "/workspace/a"], ["draft-b", "/workspace/b"]]) {
  drafts.set(id, {
    id,
    workspaceId: `workspace-${id}`,
    scope: "project",
    workspaceRoot: root,
    revision: 1,
    contentJson: "{}",
    settings,
    status: "active",
    updatedAt: 1,
  });
}

const saveStarted = deferred<void>();
const releaseSave = deferred<void>();
const beginStarted = deferred<void>();
const releaseBegin = deferred<SessionDraftSubmissionView>();
const releaseAttachment = deferred<void>();
const submissions: SessionDraftSubmissionRequest[] = [];
const themeModes: string[] = [];
let saveCalls = 0;
const server: ServerView = {
  name: "fixture-mcp", transport: "stdio", status: "deferred", enabled: true, installed: true,
  autoStart: true, tools: 0, toolCount: 0, prompts: 0, resources: 0, toolList: [],
};
const commands = [
  { name: "compact", description: "Compact", kind: "builtin" as const, draftBehavior: "unavailable" as const },
  { name: "theme", description: "Theme", kind: "builtin" as const, draftBehavior: "direct" as const },
];

const stub = installDesktopHostStub({
  ListSessionDraftSummaries: async () => [],
  OpenSessionDraftForTarget: async (_scope: string, root: string) => structuredClone(root.endsWith("/a") ? drafts.get("draft-a") : drafts.get("draft-b")),
  GetDraftContext: async (id: string) => ({ draft: structuredClone(drafts.get(id)), commands, servers: [server] }),
  SaveSessionDraft: async (request: { draftId: string; revision: number; contentJson: string; settings: typeof settings }) => {
    saveCalls++;
    saveStarted.resolve();
    await releaseSave.promise;
    const current = drafts.get(request.draftId)!;
    assert.equal(request.revision, current.revision, "concurrent flushes share one CAS save");
    const next = { ...current, revision: current.revision + 1, contentJson: request.contentJson, settings: request.settings };
    drafts.set(request.draftId, next);
    return { draft: structuredClone(next), conflict: false, outcome: "saved" };
  },
  BeginDraftSubmission: async (request: SessionDraftSubmissionRequest) => {
    submissions.push(request);
    beginStarted.resolve();
    return releaseBegin.promise;
  },
  GetDraftSubmission: async () => { throw new Error("accepted submission must not poll"); },
  AttachmentDataURLForTarget: async () => "",
  DismissSessionDraft: async () => {},
  SetSessionDraftRestoreTarget: async () => {},
  GetSessionDraft: async (id: string) => structuredClone(drafts.get(id)!),
  RestoreSessionDraft: async () => null,
  ListTabs: async () => [{ id: "formal" }],
  SetDesktopAppearance: async (mode: string) => { themeModes.push(mode); },
  GetThemeExperience: async () => ({ themeMode: themeModes.at(-1) ?? "auto", baseStyle: "graphite", effectiveStyle: "graphite" }),
});

let owner!: ReturnType<typeof useSessionDraftSurface>;
const accepted: string[] = [];
function Probe() {
  owner = useSessionDraftSurface({
    onAccepted: (ref) => { accepted.push(ref.sessionId); },
    onChanged: () => {},
  });
  return null;
}

const root = createRoot(document.getElementById("root")!);
const edited: PersistentComposerDraft = {
  text: "run A",
  invocations: [],
  attachments: [],
  workspaceRefs: [],
  pastedBlocks: [],
  openPastedLabels: [],
  sessionRefs: [],
  selectedTextRefs: [],
};

try {
  await act(async () => { root.render(<Probe />); });
  await act(async () => { await owner.open("project", "/workspace/a"); });
  act(() => owner.updateContent(edited));
  const draftAId = owner.surface!.draft.id;
  const draftAGeneration = owner.surface!.generation;

  let submit!: Promise<void>;
  let openB!: Promise<void>;
  let exitFlush!: Promise<void>;
  let exitFlushFinished = false;
  act(() => { submit = owner.submit("run A"); });
  await saveStarted.promise;
  act(() => {
    owner.updateContentFor(draftAId, draftAGeneration, { ...edited, text: "late mutation" });
    owner.updateSettingsFor(draftAId, draftAGeneration, { model: "late/model" });
  });
  await act(async () => {
    openB = owner.open("project", "/workspace/b");
    await openB;
  });
  assert.equal(owner.surface?.draft.id, "draft-b", "the test observes B after a real intermediate React commit");
  await act(async () => {
    const submittedBeforeCommands = submissions.length;
    await assert.rejects(owner.submit("/compact"), /existing session/, "history-only commands stay unavailable in a draft");
    assert.equal(submissions.length, submittedBeforeCommands, "unavailable draft commands never reserve an operation");
    await owner.submit("/theme dark");
    assert.deepEqual(themeModes, ["dark"], "direct theme command updates appearance without draft submission");
    owner.setMCPEnabled(server, false);
  });
  const draftB = owner.surface!;
  const attachmentTask = releaseAttachment.promise.then(() => {
    owner.updateContentFor(draftB.draft.id, draftB.generation, {
      ...edited,
      text: "run B",
      attachments: [{ path: ".reasonix/attachments/late.txt", displayName: "late.txt" }],
    });
  });
  act(() => { owner.trackTask(draftB.draft.id, draftB.generation, attachmentTask); });
  assert.equal(owner.surface?.pendingTasks, 1, "the captured attachment belongs to B before exit starts");
  assert.ok(window.__reasonixFlushSessionDraft, "draft owner installs the Electron exit flush hook");
  act(() => { exitFlush = window.__reasonixFlushSessionDraft!().then(() => { exitFlushFinished = true; }); });
  await Promise.resolve();
  assert.equal(exitFlushFinished, false, "exit flush waits for the active CAS save acknowledgement");
  await act(async () => {
    releaseSave.resolve();
    await beginStarted.promise;
    await Promise.resolve();
    assert.equal(exitFlushFinished, false, "exit flush also waits for captured attachment work");
    releaseAttachment.resolve();
    await attachmentTask;
    assert.equal(exitFlushFinished, false, "exit waits until the preparation has a persisted operation");
    releaseBegin.resolve({ operationId: "operation-a", draftId: "draft-a", phase: "accepted", submissionId: "submission-a", session: { hostId: "local", sessionId: "session-a" }, updatedAt: 1, revision: 1, canResume: false, canEdit: false, canCancel: true, canDiscard: false });
    await exitFlush;
  });
  assert.equal(saveCalls, 2, "A and B save independently and B flushes the attachment produced before exit");
  assert.equal(owner.surface?.draft.id, "draft-b", "navigation can reveal B after A is durably saved");
  assert.equal(submissions[0]?.draftId, "draft-a", "A submission retains its source DraftID after B becomes visible");
  assert.equal(submissions[0]?.input, "run A", "A submission retains its source payload");
  assert.equal(JSON.parse(drafts.get("draft-a")!.contentJson).text, "run A",
    "preparing submission synchronously locks the captured content");
  assert.equal(drafts.get("draft-a")!.settings.model, "fixture-model",
    "preparing submission synchronously locks the captured settings");

  await act(async () => {
    releaseBegin.resolve({
      operationId: "operation-a",
      draftId: "draft-a",
      phase: "accepted",
      submissionId: "submission-a",
      session: { hostId: "local", sessionId: "session-a" },
      updatedAt: 2,
    });
    await submit;
  });
  assert.equal(owner.surface?.draft.id, "draft-b", "accepted A does not navigate away from visible B");
  assert.deepEqual(accepted, [], "stale accepted operation does not claim visible navigation ownership");

  assert.equal(submissions.length, 1, "direct draft commands never reserve an operation");
  assert.ok(drafts.get("draft-b")?.settings.disabledMcp[server.name], "MCP selection persists in draft settings without a Controller");
  assert.equal(JSON.parse(drafts.get("draft-b")!.contentJson).attachments[0]?.path, ".reasonix/attachments/late.txt",
    "attachment completion during exit is saved before shutdown continues");

  assert.ok(window.__reasonixResumeSessionDraftEditing, "draft owner installs the exit-veto recovery hook");
  act(() => { window.__reasonixResumeSessionDraftEditing!(); });
  act(() => owner.updateContent({ ...edited, text: "editing resumed after veto" }));
  await act(async () => { await owner.flush(); });
  assert.equal(JSON.parse(drafts.get("draft-b")!.contentJson).text, "editing resumed after veto",
    "a cancelled exit releases the renderer barrier and accepts new edits");

  await act(async () => { root.unmount(); });
  assert.equal(window.__reasonixFlushSessionDraft, undefined, "unmount removes the renderer exit hook");
  assert.equal(window.__reasonixResumeSessionDraftEditing, undefined, "unmount removes the exit-veto recovery hook");
  console.log("session draft surface: save coalescing, source submission and stale acceptance isolation passed");
} finally {
  stub.uninstall();
  dom.window.close();
}
