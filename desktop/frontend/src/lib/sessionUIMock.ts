import type { ManualSessionCreationView, SessionComposerState } from "../generated/desktopContract.generated";
import type { SessionUIBindings } from "./sessionUIBindings";

export function makeSessionUIMock(create: (scope: string, root: string, id: string) => Promise<void>): SessionUIBindings {
  const operations = new Map<string, ManualSessionCreationView>();
  const inputs = new Map<string, SessionComposerState>();
  const submissions = new Set<string>();
  const read: SessionUIBindings["GetSessionComposerState"] = async ref => structuredClone(inputs.get(`${ref.hostId}:${ref.sessionId}`) ?? {
    ref, revision: "0", contentJson: "{}", contentVersion: 1, baseline: "mock", historyChanged: false, conflict: false,
  });
  const save = (state: SessionComposerState) => {
    const next = { ...state, revision: String(BigInt(state.revision) + 1n) };
    inputs.set(`${state.ref.hostId}:${state.ref.sessionId}`, next); return structuredClone(next);
  };
  return {
    async BeginManualSessionCreation(req) {
      const prior = operations.get(req.operationId); if (prior) return structuredClone(prior);
      const id = `manual-${req.operationId}`;
      const op: ManualSessionCreationView = { operationId: req.operationId, workspaceId: req.workspaceId || req.workspaceRoot || "global",
        scope: req.scope || "global", workspaceRoot: req.workspaceRoot || "", ref: { hostId: "local", sessionId: id }, topicId: id, phase: "starting",
        settings: { model: "deepseek/deepseek-chat", mode: "normal", toolApprovalMode: "default", disabledMcp: {}, mcpOrder: [] } };
      operations.set(req.operationId, op);
      await create(op.scope, op.workspaceRoot, id); op.phase = "ready"; return structuredClone(op);
    },
    async GetManualSessionCreation(id) { const op = operations.get(id); if (!op) throw new Error("Creation not found"); return structuredClone(op); },
    async ListManualSessionCreations() { return [...operations.values()].filter(op => op.phase !== "ready").map(op => structuredClone(op)); },
    async RetryManualSessionCreation(id) { return this.GetManualSessionCreation(id); },
    GetSessionComposerState: read,
    async ListSessionComposerConflicts() { return []; },
    async SaveSessionComposerState(req) {
      const state = await read(req.ref);
      if (state.revision !== req.expectedRevision) return { ...state, conflict: true };
      if (state.submissionId) throw new Error("Check pending submission");
      return save({ ...state, contentJson: req.contentJson, contentVersion: req.contentVersion, historyChanged: false });
    },
    async BeginSessionComposerSubmission(ref, revision, submissionId, request) {
      const state = await read(ref);
      if (state.submissionId === submissionId && state.submissionRevision === revision && state.submissionRequest === request) return state;
      const key = `${ref.hostId}:${ref.sessionId}/${submissionId}`;
      if (state.revision !== revision || state.submissionId || submissions.has(key)) throw new Error("Input changed");
      submissions.add(key);
      return save({ ...state, submissionId, submissionRevision: revision, submissionRequest: request, submissionPhase: "pending" });
    },
    async CompleteSessionComposerSubmission(ref, id, outcome) {
      const state = await read(ref); if (!state.submissionId) return state;
      if (state.submissionId !== id) throw new Error("Input changed");
      return save({ ...state, contentJson: outcome === "accepted" ? "{}" : state.contentJson,
        submissionId: outcome === "unknown" ? id : undefined, submissionPhase: outcome === "unknown" ? "unknown" : undefined });
    },
  };
}
