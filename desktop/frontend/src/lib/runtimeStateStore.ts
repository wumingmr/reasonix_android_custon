import type { ProjectRuntimeTopic } from "./types";
import type { PendingInteraction, RecoveryStatus, Todo } from "../generated/desktopContract.generated";
import { sameSessionIdentity, type SessionIdentity } from "./sessionIdentity";

export interface RuntimeState {
  schemaVersion: number;
  hostId?: string;
  sessionId?: string;
  sessionCodec?: string;
  projectionEpoch?: string;
  runtimeEpoch: string;
  activityRevision: number;
  revision: number;
  phase: "idle" | "executing" | "finishing" | "cancelling" | "recovery_required" | "closed";
  running: boolean;
  turnId: string;
  turnStatus: string;
  turnEventSeq: number;
  committedEventSeq?: number;
  durableEventSeq?: number;
  persistenceStatus?: "ready" | "pending" | "failed" | "uncertain" | "unavailable";
  persistenceError?: string;
  headId?: string;
  pendingPrompt: boolean;
  pendingInteractions?: PendingInteraction[];
  todos?: Todo[];
  todoWritten?: boolean;
  cancelRequested: boolean;
  cancellable: boolean;
  backgroundJobs: number;
  activity: string;
  recovery?: RecoveryStatus | null;
  maintenance?: {
    operationId: string;
    kind: string;
    activity: "running" | "cancelling" | "finalizing" | "recovery_required" | string;
    status?: string;
    operationRevision?: number;
    runtimeEpoch?: string;
    errorCode?: string;
    detail?: string;
    applied?: boolean;
    inputTokens?: number;
    resultTokens?: number;
    messages?: number;
  };
}

export function acceptSessionRuntimeSnapshot(current: RuntimeState | undefined, next: RuntimeState, allowProducerBaseline = false): RuntimeState {
  if (!current) return next;
  if (current.projectionEpoch !== next.projectionEpoch) return allowProducerBaseline ? next : current;
  if (next.revision < current.revision) return current;
  return next.revision === current.revision ? current : next;
}
export interface RuntimeSession {
  tabId: string;
  scope: string;
  workspaceRoot: string;
  topicId: string;
  sessionId?: string;
  sessionPath: string;
  sessionGeneration: number;
  open: boolean;
  remote: boolean;
  hostId?: string;
  freshness: "synced" | "unknown" | "syncing";
  state: RuntimeState;
}
export interface RuntimeProjection {
  epoch: string;
  revision: number;
  sessions: RuntimeSession[];
  topics: ProjectRuntimeTopic[];
}

/** A tab is a reusable surface, not a session identity. Unknown/blank bindings
 * must not adopt the previous session while navigation metadata catches up. */
export function selectRuntimeSession(snapshot: RuntimeProjection | undefined, tabId: string | undefined, identity: SessionIdentity | string | undefined) {
  const target = typeof identity === "string" ? { sessionPath: identity } : identity;
  if (!tabId || !target) return undefined;
  if (typeof identity !== "string" && target.sessionGeneration == null) return undefined;
  return snapshot?.sessions.find(session => session.open && session.tabId === tabId && sameSessionIdentity(target, {
    session: target.session?.sessionId && session.sessionId
      ? { hostId: session.hostId || "local", sessionId: session.sessionId }
      : undefined,
    sessionPath: session.sessionPath,
    sessionGeneration: session.sessionGeneration,
  }));
}

export function selectRuntime(session?: RuntimeSession, failed = false) {
  const state = session?.state;
  const known = state?.schemaVersion === 1;
  const unknown = Boolean(session && (failed || session.freshness !== "synced"));
  const finishing = known && state.phase === "finishing";
  const kind = unknown ? "unknown" : !known ? "legacy" : state.maintenance?.activity === "finalizing" ? "maintenance_finalizing"
    : state.maintenance?.activity === "cancelling" ? "maintenance_cancelling"
    : state.maintenance?.activity === "recovery_required" ? "recovery_required"
    : state.maintenance ? "maintenance_running" : finishing ? "finishing"
    : state.phase === "recovery_required" ? "recovery_required"
    : state.cancelRequested || state.phase === "cancelling" ? "cancelling" : state.pendingPrompt ? "waiting_confirmation"
    : state.phase === "executing" ? state.activity === "streaming" ? "streaming" : "thinking"
    : state.backgroundJobs > 0 ? "background_job" : "idle";
  return { kind, known, unknown, finishing, state,
    running: known ? state.running : undefined,
    cancellable: known ? !unknown && !finishing && state.cancellable && !state.cancelRequested : undefined,
    spinning: !unknown && (kind === "thinking" || kind === "streaming" || kind === "cancelling" || kind === "maintenance_running" || kind === "maintenance_cancelling" || kind === "maintenance_finalizing" || kind === "background_job"),
  };
}

export function createRuntimeStateStore() {
  let snapshot: RuntimeProjection | undefined;
  let failed = false;
  const listeners = new Set<() => void>();
  const notify = () => listeners.forEach(listener => listener());
  return {
    getSnapshot: () => snapshot,
    getFailed: () => failed,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener); }; },
    fail() { if (!failed) { failed = true; notify(); } },
    commit(next: RuntimeProjection) {
      if (snapshot === next && !failed) return;
      snapshot = next;
      failed = false;
      notify();
    },
  };
}
export const runtimeStateStore = createRuntimeStateStore();
