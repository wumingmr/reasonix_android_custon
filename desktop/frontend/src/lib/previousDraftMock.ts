import type { SessionDraftView, SessionDraftSubmissionView, SessionDraftSaveRequest, SessionDraftSubmissionRequest } from "../generated/desktopContract.generated";
import type { PreviousDraftBindings } from "./previousDraftBindings";

export function makePreviousDraftMock(): PreviousDraftBindings {
  const mockDrafts = new Map<string, SessionDraftView>();
  const mockDraftOperations = new Map<string, SessionDraftSubmissionView>();
  const mockDraftForTarget = (scope: string, workspaceRoot: string): SessionDraftView => {
    const normalizedScope = scope === "project" && workspaceRoot ? "project" : "global";
    const normalizedRoot = normalizedScope === "project" ? workspaceRoot : "";
    const workspaceId = normalizedScope === "global" ? "global" : `project-${normalizedRoot}`;
    const prior = [...mockDrafts.values()].find((draft) => draft.workspaceId === workspaceId && draft.status === "active");
    if (prior) return prior;
    throw new Error("No previous draft exists; create a formal conversation");
  };
  return {
    async OpenSessionDraft(workspaceId: string) {
      const prior = [...mockDrafts.values()].find((draft) => draft.workspaceId === workspaceId && draft.status === "active");
      if (prior) return structuredClone(prior);
      return structuredClone(mockDraftForTarget(workspaceId === "global" ? "global" : "project", workspaceId === "global" ? "" : workspaceId));
    },
    async OpenSessionDraftForTarget(scope: string, workspaceRoot: string) {
      return structuredClone(mockDraftForTarget(scope, workspaceRoot));
    },
    async RestoreSessionDraft() {
      return structuredClone([...mockDrafts.values()].find((draft) => draft.status === "active") ?? null);
    },
    async SaveSessionDraft(request: SessionDraftSaveRequest) {
      const current = mockDrafts.get(request.draftId);
      if (!current || current.status !== "active") throw new Error("session draft not found");
      if (current.revision !== request.revision) return { draft: structuredClone(current), conflict: true, outcome: "conflict" };
      const next = { ...current, revision: current.revision + 1, contentJson: request.contentJson, settings: structuredClone(request.settings), updatedAt: Date.now() };
      mockDrafts.set(next.id, next);
      return { draft: structuredClone(next), conflict: false, outcome: "saved" };
    },
    async ListSessionDraftSummaries() {
      return [...mockDrafts.values()].filter((draft) => draft.status === "active").map((draft) => ({
        id: draft.id, workspaceId: draft.workspaceId, scope: draft.scope, workspaceRoot: draft.workspaceRoot,
        revision: draft.revision, hasContent: draft.contentJson !== "{}", state: "saved", updatedAt: draft.updatedAt,
      }));
    },
    async DiscardSessionDraft(draftId: string, revision: number) {
      const current = mockDrafts.get(draftId);
      if (!current || current.revision !== revision) throw new Error("session draft revision conflict");
      mockDrafts.set(draftId, { ...current, status: "discarded", revision: current.revision + 1 });
    },
    async DismissSessionDraft(_draftId: string) {},
    async SetSessionDraftRestoreTarget(_draftId: string) {},
    async GetSessionDraft(draftId: string) {
      const draft = mockDrafts.get(draftId);
      if (!draft) throw new Error("session draft not found");
      return structuredClone(draft);
    },
    async GetDraftContext(draftId: string) {
      const draft = mockDrafts.get(draftId);
      if (!draft) throw new Error("session draft not found");
      return { draft: structuredClone(draft), commands: [], servers: [] };
    },
    async GetSessionDraftState(draftId: string) {
      const draft = mockDrafts.get(draftId);
      if (!draft) throw new Error("session draft not found");
      const operation = [...mockDraftOperations.values()].reverse().find(item => item.draftId === draftId);
      return structuredClone({ draft, operation });
    },
    async ResumeDraftSubmission(operationId: string, revision: number) {
      const operation = mockDraftOperations.get(operationId);
      if (!operation || operation.revision !== revision) throw new Error("operation revision conflict");
      return structuredClone(operation);
    },
    async BeginDraftSubmission(request: SessionDraftSubmissionRequest) {
      const draft = mockDrafts.get(request.draftId);
      if (!draft || draft.revision !== request.revision) throw new Error("session draft revision conflict");
      const operationId = `draft-op-mock-${mockDraftOperations.size + 1}`;
      const sessionId = `draft-session-mock-${mockDraftOperations.size + 1}`;
      const view: SessionDraftSubmissionView = { operationId, draftId: draft.id, requestId: request.requestId, revision: 1, canResume: false, canEdit: false, canCancel: false, canDiscard: false, phase: "accepted", submissionId: `draft-submit-mock-${mockDraftOperations.size + 1}`, session: { hostId: "local", sessionId }, updatedAt: Date.now() };
      mockDraftOperations.set(operationId, view);
      mockDrafts.set(draft.id, { ...draft, status: "converted", revision: draft.revision + 1 });
      return structuredClone(view);
    },
    async GetDraftSubmission(operationId: string) {
      const operation = mockDraftOperations.get(operationId);
      if (!operation) throw new Error("session draft submission not found");
      return structuredClone(operation);
    },
    async CancelDraftSubmission(operationId: string) {
      const operation = mockDraftOperations.get(operationId);
      if (!operation) throw new Error("session draft submission not found");
      const next = { ...operation, phase: operation.phase === "accepted" ? "accepted" : "cancelled", updatedAt: Date.now() };
      mockDraftOperations.set(operationId, next);
      return structuredClone(next);
    },
  };
}
