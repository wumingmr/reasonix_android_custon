import type { HistoryMessage, WireSessionOperation } from "./types";
import type { Item } from "./useController";
import type { StructuredInvocationSubmit } from "./invocationDisplay";

export type CompactionItem = Extract<Item, { kind: "compaction" }>;

/** Match the controller's management route without treating other slash inputs as commands. */
export function isCompactCommand(input: string): boolean {
  const trimmed = input.trim();
  return trimmed === "/compact" || trimmed.startsWith("/compact ");
}

/** Structured invocations and initial goals retain their own admission contracts. */
export function isCompactSubmission(input: string, structured?: StructuredInvocationSubmit, initialGoal?: object): boolean {
  return !structured && !initialGoal && isCompactCommand(input);
}

const terminalStatuses = new Set([
  "completed",
  "cancelled",
  "partially_completed",
  "failed",
  "noop",
  "interrupted",
  "recovery_required",
]);
const pendingStatuses = new Set(["running", "cancelling", "finalizing", "loading", "confirming"]);

function object(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}

function text(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function count(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : undefined;
}

function boolean(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

/** Decodes the content of a session-maintenance-v1 compaction display row. */
export function parseSessionOperation(value: unknown): WireSessionOperation | undefined {
  let raw = object(value);
  if (!raw) return undefined;
  if (!text(raw.operationId) && typeof raw.content === "string") {
    try { raw = object(JSON.parse(raw.content)) ?? raw; } catch { return undefined; }
  }
  const operationId = text(raw.operationId);
  if (!operationId) return undefined;
  return {
    operationId,
    kind: text(raw.kind) ?? "compact",
    activity: text(raw.activity) ?? "",
    status: text(raw.status) ?? "",
    errorCode: text(raw.errorCode),
    detail: text(raw.detail),
    applied: boolean(raw.applied),
    inputTokens: count(raw.inputTokens),
    resultTokens: count(raw.resultTokens),
    messages: count(raw.messages),
    summary: typeof raw.summary === "string" ? raw.summary : undefined,
    archive: typeof raw.archive === "string" ? raw.archive : undefined,
    operationRevision: count(raw.operationRevision),
    runtimeEpoch: text(raw.runtimeEpoch),
  };
}

export function isTerminalSessionOperation(status: string | undefined): boolean {
  return Boolean(status && terminalStatuses.has(status));
}

export function sessionOperationStatus(status?: string, activity?: string): string {
  const value = status || activity || "unavailable";
  return isTerminalSessionOperation(value) || pendingStatuses.has(value)
    ? value : "unavailable";
}

function operationPending(status: string | undefined): boolean {
  return pendingStatuses.has(status ?? "");
}

export function sessionOperationFromHistory(message: HistoryMessage): WireSessionOperation | undefined {
  if (message.role !== "compaction" || !message.operationId) return undefined;
  const status = sessionOperationStatus(message.operationStatus, message.operationActivity);
  const awaitingRuntime = Boolean(message.pending && operationPending(status) && status !== "loading");
  return {
    operationId: message.operationId,
    kind: message.operationKind ?? "compact",
    activity: message.operationActivity ?? "",
    status: awaitingRuntime ? "confirming" : status,
    errorCode: message.errorCode,
    detail: message.detail,
    applied: message.applied,
    inputTokens: message.inputTokens,
    resultTokens: message.resultTokens,
    messages: message.messages,
    summary: message.summary,
    archive: message.archive,
    operationRevision: message.operationRevision,
    runtimeEpoch: message.runtimeEpoch,
  };
}

export function sessionOperationHistoryMessage(messageId: string, operation: WireSessionOperation): HistoryMessage {
  const status = sessionOperationStatus(operation.status, operation.activity);
  return {
    role: "compaction",
    messageId,
    content: "",
    pending: operationPending(status),
    trigger: "manual",
    messages: operation.messages,
    summary: operation.summary,
    archive: operation.archive,
    operationId: operation.operationId,
    operationKind: operation.kind,
    operationStatus: status,
    operationActivity: operation.activity || undefined,
    operationRevision: operation.operationRevision,
    runtimeEpoch: operation.runtimeEpoch,
    errorCode: operation.errorCode,
    detail: operation.detail,
    applied: operation.applied,
    inputTokens: operation.inputTokens,
    resultTokens: operation.resultTokens,
  };
}

export function sessionOperationItem(operation: WireSessionOperation, historyEntryId?: string): CompactionItem {
  const status = sessionOperationStatus(operation.status, operation.activity);
  return {
    ...operation,
    kind: "compaction",
    id: `maintenance:${operation.operationId}`,
    pending: operationPending(status),
    trigger: "manual",
    messages: operation.messages ?? 0,
    summary: operation.summary ?? "",
    archive: operation.archive ?? "",
    operationKind: operation.kind,
    status,
    activity: operation.activity || undefined,
    historyEntryId,
  };
}

function fillMissing(prior: CompactionItem, incoming: CompactionItem): CompactionItem {
  return {
    ...prior,
    operationKind: prior.operationKind || incoming.operationKind,
    errorCode: prior.errorCode ?? incoming.errorCode,
    detail: prior.detail ?? incoming.detail,
    applied: prior.applied || incoming.applied || undefined,
    inputTokens: prior.inputTokens ?? incoming.inputTokens,
    resultTokens: prior.resultTokens ?? incoming.resultTokens,
    messages: prior.messages || incoming.messages,
    summary: prior.summary || incoming.summary,
    archive: prior.archive || incoming.archive,
    operationRevision: prior.operationRevision ?? incoming.operationRevision,
    runtimeEpoch: prior.runtimeEpoch ?? incoming.runtimeEpoch,
    historyEntryId: prior.historyEntryId ?? incoming.historyEntryId,
  };
}

/**
 * Merges durable history, live events and runtime snapshots for one operation.
 * Missing fields are partial observations; revisions and terminality are
 * monotonic so a delayed progress update cannot reopen a completed card.
 */
export function mergeSessionOperationItem(prior: CompactionItem | undefined, incoming: CompactionItem): CompactionItem {
  if (!prior || prior.operationId !== incoming.operationId) return incoming;
  const priorRevision = prior.operationRevision;
  const incomingRevision = incoming.operationRevision;
  const older = priorRevision !== undefined && incomingRevision !== undefined && incomingRevision < priorRevision;
  const epochMismatch = Boolean(prior.runtimeEpoch && incoming.runtimeEpoch && prior.runtimeEpoch !== incoming.runtimeEpoch);
  const terminalRegression = !prior.interruptionInferred && isTerminalSessionOperation(prior.status) && !isTerminalSessionOperation(incoming.status);
  if (older || epochMismatch || terminalRegression) return fillMissing(prior, incoming);

  const merged: CompactionItem = {
    ...prior,
    ...Object.fromEntries(Object.entries(incoming).filter(([, value]) => value !== undefined)),
    id: prior.id,
    messages: incoming.messages || prior.messages,
    summary: incoming.summary || prior.summary,
    archive: incoming.archive || prior.archive,
    applied: prior.applied || incoming.applied || undefined,
    interruptionInferred: incoming.interruptionInferred,
  } as CompactionItem;
  merged.pending = operationPending(merged.status);
  return merged;
}

/** Upserts by operation identity and collapses any legacy duplicate cards. */
export function upsertSessionOperationItem(items: Item[], incoming: CompactionItem): { items: Item[]; inserted: boolean } {
  const indexes: number[] = [];
  items.forEach((item, index) => {
    if (item.kind === "compaction" && item.operationId === incoming.operationId) indexes.push(index);
  });
  if (indexes.length === 0) return { items: [...items, incoming], inserted: true };
  let merged = incoming;
  for (const index of indexes) merged = mergeSessionOperationItem(items[index] as CompactionItem, merged);
  const first = indexes[0];
  return {
    items: items.flatMap((item, index) => index === first ? [merged] : indexes.includes(index) ? [] : [item]),
    inserted: false,
  };
}

/**
 * Reconciles a freshly loaded history window with operation updates that may
 * have arrived while the read was in flight. Only matching identities merge;
 * an active card absent from the window is retained as a live tail.
 */
export function reconcileSessionOperationItems(history: Item[], live: Item[], preserveMissing = false): Item[] {
  let result = history;
  for (const item of live) {
    if (item.kind !== "compaction" || !item.operationId) continue;
    if (result.some(candidate => candidate.kind === "compaction" && candidate.operationId === item.operationId)) {
      result = upsertSessionOperationItem(result, item).items;
    } else if (preserveMissing && item.pending) {
      result = [...result, item];
    }
  }
  return result;
}

/**
 * Resolves durable progress rows once a runtime snapshot has established the
 * active maintenance identity. A history row remains pending until that first
 * snapshot arrives; afterwards only the matching runtime operation may remain
 * pending. This is deliberately a display-only recovery decision.
 */
export function interruptOrphanedSessionOperationItems(
  items: Item[],
  activeOperationId?: string,
  activeRuntimeEpoch?: string,
  runtimeRevision?: number,
): Item[] {
  let changed = false;
  const next = items.map((item) => {
    if (item.kind !== "compaction" || !item.operationId || !item.pending || item.status === "loading" || isTerminalSessionOperation(item.status)) return item;
    // A snapshot cached before this live event cannot declare it orphaned.
    // A changed epoch is independently authoritative even if revisions reset.
    if ((!item.runtimeEpoch || item.runtimeEpoch === activeRuntimeEpoch) && runtimeRevision !== undefined
      && item.observedRuntimeRevision !== undefined && runtimeRevision <= item.observedRuntimeRevision) return item;
    const identityMatches = item.operationId === activeOperationId
      && !(item.runtimeEpoch && activeRuntimeEpoch && item.runtimeEpoch !== activeRuntimeEpoch);
    if (identityMatches) return item;
    changed = true;
    return { ...item, pending: false, status: "interrupted", activity: "interrupted", interruptionInferred: true };
  });
  return changed ? next : items;
}
