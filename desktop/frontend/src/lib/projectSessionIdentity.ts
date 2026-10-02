import type { ProjectNode } from "./types";
import { sessionIdentityBaseKey } from "./sessionIdentity";

/** Stable row identity; topicId is only the last resort for an empty draft. */
export function projectSessionIdentity(node: ProjectNode): string {
  if (node.session?.sessionId) return sessionIdentityBaseKey(node);
  if (node.remoteSession) {
    const remote = node.remoteSession;
    if (remote.sessionId) return sessionIdentityBaseKey({ session: { hostId: remote.hostId, sessionId: remote.sessionId } });
    return `source\u0000${remote.hostId}\u0000${remote.workspace}\u0000${remote.path || remote.name}`;
  }
  if (node.source) return `source\u0000${node.source.hostId || "local"}\u0000${node.source.sourceKey || `${node.source.path}\u0000${node.source.headId || ""}`}`;
  if (node.sessionPath) return `path\u0000${node.sessionPath.trim()}`;
  if (node.tabId) return `tab\u0000local\u0000${node.tabId}`;
  return `topic\u0000${node.topicId || node.key}`;
}

/** Only owner-verified aliases may join a source to its adopted session. */
export function projectSessionKeys(node: ProjectNode): string[] {
  return [projectSessionIdentity(node), ...(node.identityAliases ?? [])];
}

export function projectSessionExcluded(node: ProjectNode, excluded: ReadonlySet<string>): boolean {
  return projectSessionKeys(node).some(key => excluded.has(key)) || Boolean(node.topicId && excluded.has(node.topicId));
}

/** A source keeps its mounted row key when its owner publishes canonical aliases. */
const mountedRowKeys = new Map<string, string>();
export function projectSessionRowKey(node: ProjectNode): string {
  const keys = projectSessionKeys(node);
  const existing = keys.map(key => mountedRowKeys.get(key)).find(Boolean);
  const rowKey = existing ?? projectSessionIdentity(node);
  for (const key of keys) mountedRowKeys.set(key, rowKey);
  return rowKey;
}

export function sameProjectSession(a: ProjectNode, b: ProjectNode): boolean {
  const keys = new Set(projectSessionKeys(a));
  return projectSessionKeys(b).some(key => keys.has(key));
}
