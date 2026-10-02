import type { ProjectNode } from "./types";
import type { DictKey } from "./i18n";
import type { SessionSelector } from "../generated/desktopContract.generated";

// Keep the RPC's string argument compatible with older topic callers while
// selecting the exact durable row whenever the catalog supplies its identity.
export function sessionTitleTarget(node: ProjectNode): string {
  if (node.session?.sessionId) return `session-id:${node.session.sessionId}`;
  if (node.source) return `session-source:${encodeURIComponent(JSON.stringify(node.source))}`;
  return node.sessionPath?.trim() || node.topicId?.trim() || "";
}

export function sessionTitleSelector(target: string): SessionSelector {
  const value = target.trim();
  if (value.startsWith("session-source:")) return { source: JSON.parse(decodeURIComponent(value.slice("session-source:".length))) };
  if (value.startsWith("session-id:")) {
    return { ref: { hostId: "local", sessionId: value.slice("session-id:".length) } };
  }
  if (value.includes("/") || value.includes("\\")) return { sessionPath: value };
  return { topicId: value };
}

export function sessionTitleErrorKey(error: unknown): DictKey {
  const record = typeof error === "object" && error !== null ? error as Record<string, unknown> : undefined;
  const data = record && typeof record.data === "object" && record.data !== null
    ? record.data as Record<string, unknown>
    : undefined;
  const message = error instanceof Error ? error.message : String(error);
  const code = typeof data?.sessionCode === "string"
    ? data.sessionCode
    : message.match(/session_operation:([a-z_]+):/)?.[1];
  const keys: Record<string, DictKey> = {
    target_not_found: "projectTree.sessionError.targetNotFound", no_messages: "projectTree.sessionError.noMessages",
    ambiguous_target: "projectTree.sessionError.ambiguousTarget",
    runtime_not_open: "projectTree.sessionError.runtimeNotOpen", runtime_not_ready: "projectTree.sessionError.runtimeNotReady",
    remote_disconnected: "projectTree.sessionError.remoteDisconnected", title_conflict: "projectTree.sessionError.titleConflict",
    target_changed: "projectTree.sessionError.targetChanged", archived: "projectTree.sessionError.archived",
    operation_busy: "projectTree.sessionError.operationBusy", provider_unavailable: "projectTree.sessionError.providerUnavailable",
    stale_cursor: "projectTree.sessionError.staleCursor", unsupported: "projectTree.sessionError.unsupported",
    operation_failed: "projectTree.sessionError.failed", session_damaged: "projectTree.sessionError.damaged",
    source_unavailable: "projectTree.sessionError.sourceUnavailable",
    source_ambiguous: "projectTree.sessionError.sourceAmbiguous",
  };
  // Providers, disk errors and older hosts can return paths or credentials.
  // Never render unclassified backend details in the product toast.
  return keys[code ?? ""] ?? "projectTree.sessionError.failed";
}
