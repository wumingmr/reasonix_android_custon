import type { PersistentMessage } from "../generated/desktopContract.generated";
import { HistoryPreparingError } from "./historyPreparation";
import { canonicalSteerGuidance, canonicalUserDisplay } from "./canonicalUserDisplay";
import { isHostRecoveryGuidance } from "./hostRecoverySteer";
import { parseSessionOperation, sessionOperationHistoryMessage } from "./sessionMaintenanceOperation";
import type { HistoryContentChunk, HistoryContentRef, HistoryEntry, HistoryMessage, HistorySlice, HistorySliceRequest, HistoryWindowPageView, HistoryWindowRequestView, MemoryCitation } from "./types";

const contentRecovery = new Map<string, () => void>();
export function registerTranscriptContentRecovery(tabId: string, recover: () => void): () => void {
  contentRecovery.set(tabId, recover);
  return () => { if (contentRecovery.get(tabId) === recover) contentRecovery.delete(tabId); };
}

function asWireObject(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

export function canonicalMessage(message: PersistentMessage, body: unknown): HistoryMessage {
  const raw = asWireObject(body);
  if (raw.role === "compaction" || message.role === "compaction") {
    const operation = parseSessionOperation(raw);
    if (operation) return sessionOperationHistoryMessage(String(raw.id ?? message.messageId), operation);
    const operationId = message.messageId.startsWith("maintenance:") ? message.messageId.slice("maintenance:".length) : message.messageId;
    return sessionOperationHistoryMessage(message.messageId, {
      operationId,
      kind: "compact",
      activity: message.contentRef ? "loading" : "unavailable",
      status: message.contentRef ? "loading" : "unavailable",
      errorCode: message.contentRef ? undefined : "record_incomplete",
    });
  }
  if (raw.role === "notice") return { role: "notice", messageId: String(raw.id ?? message.messageId), content: String(raw.content ?? ""), detail: typeof raw.detail === "string" ? raw.detail : undefined, code: typeof raw.code === "string" ? raw.code : undefined, level: raw.level === "warn" ? "warn" : "info" };
  const decisionReceipt = asWireObject(raw.decision_receipt);
  if (Object.keys(decisionReceipt).length > 0) {
    return { role: "notice", messageId: String(raw.id ?? message.messageId), content: "", code: "decision_receipt", level: "info", decisionReceipt: decisionReceipt as unknown as HistoryMessage["decisionReceipt"] };
  }
  const readPause = asWireObject(raw.read_pause);
  if (Boolean(raw.local_only) && Object.keys(readPause).length > 0) {
    return { role: "notice", messageId: String(raw.id ?? message.messageId), content: "", code: "incomplete_read", level: "info", readPause: readPause as unknown as HistoryMessage["readPause"] };
  }
  const readiness = asWireObject(raw.final_readiness_recovery);
  if (Boolean(raw.local_only) && readiness.pending === true) {
    return { role: "notice", messageId: String(raw.id ?? message.messageId), content: "Final checks are still required before this task is complete.", code: "historical_checks", level: "info", readiness: { missing: Array.isArray(readiness.missing) ? readiness.missing.map(String) : undefined } };
  }
  const protocolRecovery = asWireObject(raw.protocol_recovery);
  if (Boolean(raw.local_only) && protocolRecovery.state === "pending" && typeof protocolRecovery.id === "string") {
    return { role: "notice", messageId: String(raw.id ?? message.messageId), content: "", code: "protocol_recovery", level: "info", pending: true, protocolRecovery: { id: protocolRecovery.id } };
  }
  if (Boolean(raw.local_only)) {
    const guidance = canonicalSteerGuidance(String(raw.content ?? message.preview ?? ""));
    if (guidance !== undefined) {
      if (isHostRecoveryGuidance(guidance)) return { role: "hidden", messageId: String(raw.id ?? message.messageId), content: "" };
      // Notice formatting reads guidance after the first newline. Keep that
      // separator even when the user's guidance itself spans several lines.
      return { role: "notice", messageId: String(raw.id ?? message.messageId), content: `\n${guidance}`, code: "unapplied_steer", level: "warn" };
    }
  }
  const toolCalls = (Array.isArray(raw.tool_calls) ? raw.tool_calls as Record<string, unknown>[] : []).map(call => ({
    resultObservation: message.toolObservations?.[String(call.id ?? "")],
    id: String(call.id ?? ""), name: String(call.name ?? ""), arguments: String(call.arguments ?? ""),
    resolvedName: typeof call.resolved_name === "string" ? call.resolved_name : undefined,
    capabilityId: typeof call.capability_id === "string" ? call.capability_id : undefined,
    resolvedReadOnly: typeof call.resolved_read_only === "boolean" ? call.resolved_read_only : undefined,
    diff: typeof call.diff === "string" ? call.diff : undefined,
    added: typeof call.added === "number" ? call.added : undefined,
    removed: typeof call.removed === "number" ? call.removed : undefined,
  }));
  const presented = asWireObject(raw.presented_files);
  const role = Boolean(raw.local_only) ? "assistant" : String(raw.role ?? message.role);
  const display = role === "user" ? canonicalUserDisplay(raw, message.preview ?? "") : { role, content: String(raw.content ?? raw.raw_content ?? message.preview ?? "") };
  return {
    turnFinal: message.turnFinal ?? false,
    samplingCount: message.samplingCount ?? undefined,
    toolCount: message.toolCount ?? undefined,
    turnDurationMs: message.turnDurationMs,
    role: display.role,
    messageId: String(raw.id ?? message.messageId),
    submissionId: message.submissionId,
    content: display.content,
    reasoning: typeof raw.reasoning_content === "string" ? raw.reasoning_content : undefined,
    createdAt: typeof raw.createdAt === "number" ? raw.createdAt : undefined,
    workDurationMs: typeof raw.workDurationMs === "number" ? raw.workDurationMs : undefined,
    toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
    toolCallId: typeof raw.tool_call_id === "string" ? raw.tool_call_id : undefined,
    toolName: typeof raw.name === "string" ? raw.name : undefined,
    memoryCitations: Array.isArray(raw.memoryCitations) ? raw.memoryCitations as MemoryCitation[] : undefined,
    serverSearch: Array.isArray(raw.server_search) ? raw.server_search as HistoryMessage["serverSearch"] : undefined,
    execution: Object.keys(asWireObject(raw.tool_execution)).length > 0 ? raw.tool_execution as HistoryMessage["execution"] : undefined,
    presentedFiles: Array.isArray(presented.files) ? presented.files as HistoryMessage["presentedFiles"] : undefined,
    readCompletion: Object.keys(asWireObject(raw.read_completion)).length > 0 ? raw.read_completion as HistoryMessage["readCompletion"] : undefined,
  };
}

export function resolvedHistoryField(message: HistoryMessage, field: string): string | undefined {
  switch (field) {
    case "content": return message.content;
    case "reasoning": return message.reasoning;
    case "submitText": return message.submitText;
    case "detail": return message.detail;
    case "code": return message.code;
    case "summary": return message.summary;
    case "archive": return message.archive;
    case "toolResultError": return message.toolResultError;
    default: return message.content;
  }
}

// ── binding identity ────────────────────────────────────────────────────────
// A tab's history comes from exactly one place: the host that owns its
// binding. Crossing over on a failed call would let a transient local error
// (busy, conflict, timeout) be answered by a different service holding
// different data, so routing is decided by identity before the request, never
// by the outcome of one.
export type TranscriptBindingIdentity = "local" | "remote";

let bindingIdentityFor: ((tabId: string) => TranscriptBindingIdentity) | undefined;

/** Installed by the app layer, which is where tab metadata lives. */
export function setTranscriptBindingIdentity(resolver: (tabId: string) => TranscriptBindingIdentity): void {
  bindingIdentityFor = resolver;
}

// An unregistered identity is a tab with no remote binding, which is what a
// local session is. This is a default, not a fallback: it never moves a
// request to the other service because the first one answered badly.
function identityFor(tabId: string): TranscriptBindingIdentity {
  try {
    return bindingIdentityFor?.(tabId) ?? "local";
  } catch {
    return "local";
  }
}

export function entriesFor(messages: PersistentMessage[], snapshotSequence: number): HistoryEntry[] {
  return messages.map(persistent => {
    const entryId = `m:${persistent.messageId}`;
    return {
      entryId, turn: persistent.visibleTurn ?? 0, order: persistent.position,
      message: canonicalMessage(persistent, persistent.inline),
      refs: persistent.contentRef ? [{
        entryId, field: "canonicalMessage", size: persistent.contentRef.bytes,
        chunks: Math.max(1, Math.ceil(persistent.contentRef.bytes / (1 << 20))),
        revision: snapshotSequence, revKnown: true, digest: persistent.contentRef.digest,
        canonicalRef: persistent.contentRef,
      }] : [],
    };
  });
}

export async function canonicalHistoryWindow(tabId: string, req: HistoryWindowRequestView): Promise<HistoryWindowPageView> {
  const { readNativeTranscriptWindow } = await import("./nativeTranscriptHistory");
  const native = await readNativeTranscriptWindow(tabId, req);
  if (native) return native;
  const remote = identityFor(tabId) === "remote";
  const { readCanonicalHistoryWindow } = await import("./canonicalHistoryWindow");
  return readCanonicalHistoryWindow(tabId, req, remote);
}

function staleSlice(): HistorySlice {
  return { entries: [], nextCursor: "", hasOlder: false, hasNewer: false, newerCursor: "", totalTurns: 0, startTurn: 0, endTurn: 0, stale: true, revision: 0 };
}

/** A window page in the page-shaped form the resident store already consumes. */
function sliceFromWindow(window: HistoryWindowPageView, source: string): HistorySlice {
  return {
    entries: window.entries,
    nextCursor: window.olderCursor,
    hasOlder: window.hasOlder,
    newerCursor: window.newerCursor,
    hasNewer: window.hasNewer,
    totalTurns: window.totalTurns,
    startTurn: window.startTurn,
    endTurn: window.endTurn,
    stale: false,
    revision: window.revision,
    revisionKnown: window.revisionKnown,
    digest: window.digest,
    source,
  };
}

// turnWindowStatus maps a window status onto the page contract the store
// already understands. Empty pages alone are never an error, and a stale
// cursor is an answer rather than a failure.
function requireReadyWindow(window: HistoryWindowPageView): HistorySlice | undefined {
  switch (window.status) {
    case "ready": return sliceFromWindow(window, "window");
    case "stale_cursor": return staleSlice();
    case "preparing": throw new HistoryPreparingError();
    case "failed": throw new Error("Session history is failed");
    case "not_found": throw new Error("Session history is unavailable for this session");
    default: return undefined;
  }
}

export async function canonicalHistorySlice(tabId: string, req: HistorySliceRequest): Promise<HistorySlice> {
  const cursor = req.cursor ?? "";
  const limit = Math.min(100, Math.max(1, req.entries ?? 32));
  if (cursor.startsWith("reasonix:message:")) {
    const [id, sequence, generation] = cursor.slice("reasonix:message:".length).split(":");
    const messageId = decodeURIComponent(id);
    const snapshotSequence = Number(sequence);
    if (!Number.isSafeInteger(snapshotSequence) || snapshotSequence < 0 || generation === undefined) return staleSlice();
    const window = await canonicalHistoryWindow(tabId, { anchor: "message", messageId, snapshotSequence,
      generation: decodeURIComponent(generation), direction: req.newer ? "newer" : "older", limit });
    const ready = requireReadyWindow(window);
    if (!ready) throw new Error("Transcript v2 requires an updated Desktop and Serve");
    return ready;
  }
  // A cursor names a position inside a fixed canonical history window.
  if (cursor !== "" || req.newer) {
    const window = await canonicalHistoryWindow(tabId, {
      anchor: "cursor",
      cursor,
      direction: req.newer ? "newer" : "older",
      limit,
    });
    const ready = requireReadyWindow(window);
    if (ready) return ready;
    throw new Error("Transcript v2 requires an updated Desktop and Serve");
  }
  // The newest page carries the cursor for the same bidirectional window.
  const window = await canonicalHistoryWindow(tabId, { anchor: "newest", direction: "older", limit });
  const ready = requireReadyWindow(window);
  if (ready) return { ...ready, source: "recent" };
  throw new Error("Transcript v2 requires an updated Desktop and Serve");
}

/** Deferred bodies are loaded only after a transcript requests their content. */
export async function canonicalHistoryContent(tabID: string, ref: HistoryContentRef, chunkIndex: number): Promise<HistoryContentChunk> {
  const remote = identityFor(tabID) === "remote";
  const recover = contentRecovery.get(tabID);
  const { readCanonicalHistoryContent } = await import("./canonicalHistoryContent");
  return readCanonicalHistoryContent(tabID, ref, chunkIndex, remote, () => {
    if (contentRecovery.get(tabID) === recover) recover?.();
  });
}
