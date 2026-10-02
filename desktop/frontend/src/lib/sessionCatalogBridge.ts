import { asArray } from "./array";
import { desktopHost } from "./desktopHost";
import { mockProjectGroups } from "./mockProjectTreeOrganization";
import { mockReadSnapshotPage, releaseMockReadSnapshot } from "./mockReadSnapshot";
import type {
  ProjectNode,
  ProjectTopicKey,
  ProjectTopicPage,
  ProjectTopicPageRequest,
  ProjectTreeChangedV2,
  SessionCatalogBindings,
} from "./types";

export function onProjectTreeChangedV2(cb: (event: ProjectTreeChangedV2) => void): () => void {
  const host = desktopHost();
  if (host.kind !== "none") {
    return host.events.on("project-tree:changed-v2", (payload?: unknown) => {
      if (!payload || typeof payload !== "object") return;
      const event = payload as Partial<ProjectTreeChangedV2>;
      cb({
        revision: typeof event.revision === "number" ? event.revision : 0,
        roots: asArray(event.roots).filter((root): root is string => typeof root === "string"),
        reason: typeof event.reason === "string" ? event.reason : "changed",
      });
    });
  }
  return () => {};
}

export function makeMockSessionCatalogBindings(cloneProjectTree: () => ProjectNode[]): SessionCatalogBindings {
  const listProjectTopics = async (req: ProjectTopicPageRequest): Promise<ProjectTopicPage> => {
    const folder = req.scope === "global"
      ? cloneProjectTree().find((item) => item.kind === "global_folder")
      : cloneProjectTree().find((item) => item.kind === "project" && item.root === req.workspaceRoot);
    const query = (req.query ?? "").trim().toLocaleLowerCase();
    const created = req.sortMode === "created";
    const groups = mockProjectGroups(req.scope, req.workspaceRoot ?? "");
    const grouped = new Set(groups.flatMap((group) => group.topicIds ?? []));
    const selectedGroup = groups.find((group) => group.id === req.groupId);
    const all = asArray(folder?.children)
      .filter((item) => !query || item.label.toLocaleLowerCase().includes(query))
      .filter((item) => !req.excludePinned || !item.pinned)
      .filter((item) => req.groupFilter !== "ungrouped" || !grouped.has(item.topicId ?? ""))
      .filter((item) => req.groupFilter !== "group" || Boolean(selectedGroup?.topicIds?.includes(item.topicId ?? "")))
      .sort((left, right) => Number(Boolean(right.pinned)) - Number(Boolean(left.pinned))
        || (created ? right.createdAt || right.lastActivityAt || 0 : right.lastActivityAt || right.createdAt || 0)
          - (created ? left.createdAt || left.lastActivityAt || 0 : left.lastActivityAt || left.createdAt || 0)
        || (left.topicId ?? "").localeCompare(right.topicId ?? ""));
    return {
      ...mockReadSnapshotPage("project-topics", [req.scope, req.workspaceRoot, query, req.sortMode, req.groupFilter, req.groupId, req.excludePinned], req.cursor, req.limit, all),
      revision: 1,
      complete: true,
      readyDirectories: 1,
      pendingDirectories: 0,
      failedDirectories: 0,
    };
  };
  return {
    async ReleaseReadSnapshot(id: string) { releaseMockReadSnapshot(id); },
    async GetProjectTreeSnapshot() {
      return {
        revision: 1,
        projects: cloneProjectTree().map((project) => ({
          ...project,
          children: asArray(project.children).filter((topic) => Boolean(topic.pinned)),
        })),
        catalog: { state: "ready", mode: "memory", revision: 1, indexed: 4, total: 4, repairPending: 0, sourceCount: 4, unindexedTargetCount: 0, canRebuild: false },
        indexed: 4,
        total: 4,
        indexingDone: true,
      };
    },
    ListProjectTopics: listProjectTopics,
    async GetTopicSummary(key: ProjectTopicKey) {
      const page = await listProjectTopics({ scope: key.scope, workspaceRoot: key.workspaceRoot, limit: 200 });
      return page.items.find((item) => item.topicId === key.topicId)
        ?? { key: "", kind: key.scope === "global" ? "global_topic" : "topic", label: "", children: [] };
    },
    async GetSessionCatalogStatus() {
      return { state: "ready", mode: "memory", revision: 1, indexed: 4, total: 4, repairPending: 0, sourceCount: 4, unindexedTargetCount: 0, canRebuild: false };
    },
    async RebuildSessionCatalog() {},
  };
}
