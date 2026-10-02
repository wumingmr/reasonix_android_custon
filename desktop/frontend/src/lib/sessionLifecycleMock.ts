import type { AppBindings } from "./bridge";
import type { MockSessionLifecycleBindings } from "./sessionLifecycleBindings";
import type { SessionRef } from "./sessionRef";
import type { WorkspaceSnapshot } from "../generated/desktopContract.generated";
import type { ProjectNode } from "./types";
import { trash, recover, inspect, remove } from "./topicRemovalMock";
export function makeMockSessionLifecycleBindings(mockWorkspaceSnapshot: () => WorkspaceSnapshot, mockArchivedSessionIDs: Set<string>, mockPurgedSessionIDs: Set<string>, notifyMockProjectTreeChanged: () => void, tree: ProjectNode[] = []): MockSessionLifecycleBindings {
  const mockLifecycleResults = new Map<string, { request: string; result: import("../generated/desktopContract.generated").SessionLifecycleResult }>();
  return {
    async InspectTopicRemoval(target) { return inspect(tree, target); },
    async RemoveTopic(request) { return remove(tree, request); },
    async PurgeCanonicalSession(ref: SessionRef) {
      if (!mockArchivedSessionIDs.has(ref.sessionId) && !mockPurgedSessionIDs.has(ref.sessionId)) throw new Error("Only archived sessions can be permanently deleted");
      mockPurgedSessionIDs.add(ref.sessionId); mockArchivedSessionIDs.delete(ref.sessionId); notifyMockProjectTreeChanged();
    },
    async ListTrashEntries(this: AppBindings, query, cursor, limit) {
      const items: import("../generated/desktopContract.generated").TrashEntry[] = [];
      for (const workspace of mockWorkspaceSnapshot().workspaces) {
        const page = await this.ListWorkspaceSessions(workspace.id, query, "", 1000, true);
        items.push(...page.sessions.filter(row => row.archived).map(row => ({ id: row.ref.sessionId, ref: row.ref, title: row.title,
          workspaceId: workspace.id, workspaceTitle: workspace.title, archivedAt: 0, health: "ready", canPreview: true, canRestore: true, canPurge: true })));
      }
      const metadata = trash(tree);
      items.push(...metadata.items.filter(item => item.title.toLowerCase().includes(query.toLowerCase())));
      const offset = Number(cursor || 0), end = offset + limit;
      return { items: items.slice(offset, end), generation: metadata.generation, nextCursor: end < items.length ? String(end) : "" };
    },
    async ApplySessionLifecycle(this: AppBindings, request) {
      const previous = mockLifecycleResults.get(request.operationId), encoded = JSON.stringify(request);
      if (previous) { if (previous.request !== encoded) throw new Error("Lifecycle request conflict"); return previous.result; }
      const result: import("../generated/desktopContract.generated").SessionLifecycleResult = { operationId: request.operationId, generation: 1, committed: true, items: [] };
      for (const target of request.targets) {
        if (target.recoveryEntryId?.startsWith("topic-removal:")) {
          if (!target.workspaceId) throw new Error("Missing recovery workspace");
          recover(tree, target.recoveryEntryId, target.workspaceId, request.action);
          result.items.push({ target, workspaceId: target.workspaceId, committed: true, retryable: false });
          result.generation = trash(tree).generation;
          continue;
        }
        if (!target.ref) throw new Error("Unknown mock recovery entry");
        if (request.action === "archive") await this.ArchiveCanonicalSession(target.ref);
        else if (request.action === "restore") await this.RestoreCanonicalSession(target.ref);
        else await this.PurgeCanonicalSession(target.ref);
        const workspace = mockWorkspaceSnapshot().workspaces.find(row => row.sessionIds.includes(target.ref!.sessionId));
        result.items.push({ target, ref: target.ref, workspaceId: workspace?.id || "global", committed: true, retryable: false });
      }
      mockLifecycleResults.set(request.operationId, { request: encoded, result });
      return result;
    },
    async ListRecoveryEntries() { return { items: [], generation: 1 }; },
    async PreviewRecoveryEntry() { return { messages: [], startTurn: 0, endTurn: 0, totalTurns: 0, hasOlder: false }; },
    async RestoreRecoveryEntry() { throw new Error("recovery entry is unavailable"); },
    async GetSessionUpgradeStatus() { return { sources: 0, sessions: 0, operations: 0, discovered: 0, migrated: 0, pending: 0, failed: 0, conflicts: 0, pendingOperations: 0 }; },
  };
}
