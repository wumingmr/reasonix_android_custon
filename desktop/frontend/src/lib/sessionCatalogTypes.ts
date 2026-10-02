import type { ProjectNode } from "./types";

export interface SessionCatalogStatus {
  state: "opening" | "ready" | "degraded" | "rebuilding" | "closed" | string;
  mode?: "disk" | "memory" | string;
  revision: number;
  indexed: number;
  total: number;
  repairPending: number;
  repairActive?: number;
  repairDeferred?: number;
  repairBlocked?: number;
  nextRepairAt?: number;
  repairReason?: string;
  sourceCount?: number;
  unindexedTargetCount?: number;
  lastRepairAt?: number;
  canRebuild?: boolean;
  lastError?: string;
  quarantinedPath?: string;
}

export interface ProjectTreeSnapshot {
  revision: number;
  workspaceGeneration?: number | null;
  projects: ProjectNode[];
  catalog: SessionCatalogStatus;
  indexed: number;
  total: number;
  indexingDone: boolean;
}

export interface ProjectTopicPageRequest {
  scope: "global" | "project" | string;
  workspaceRoot?: string;
  cursor?: string;
  limit?: number;
  query?: string;
  timeFilter?: string;
  sortMode?: "created" | "updated" | string;
  groupFilter?: "all" | "ungrouped" | "group" | string;
  groupId?: string;
  excludePinned?: boolean;
}

export interface ProjectTopicPage {
  snapshotId?: string;
  snapshotExpiresAt?: number;
  replacedSnapshot?: boolean;
  items: ProjectNode[];
  nextCursor?: string;
  revision: number;
  complete?: boolean;
  readyDirectories?: number;
  pendingDirectories?: number;
  failedDirectories?: number;
}

export interface ProjectTopicKey {
  scope: "global" | "project" | string;
  workspaceRoot?: string;
  topicId: string;
  path?: string;
  recordClassification?: boolean;
}

export interface ProjectTreeChangedV2 {
  revision: number;
  roots: string[];
  reason: string;
}

export interface ProjectRuntimeTopic {
  scope: "global" | "project" | string;
  workspaceRoot?: string;
  node: ProjectNode;
}

export interface ProjectTreeRuntimeSnapshot {
  revision: number;
  topics: ProjectRuntimeTopic[];
}

export interface SessionGroup {
  id: string;
  title: string;
  topicIds?: string[];
  sessionKeys?: string[];
  excludedSessionKeys?: string[];
}

export interface ProjectGroupsSnapshot {
  groups: SessionGroup[];
  revision: number;
  applied: boolean;
}

export interface SessionCatalogBindings {
  ReleaseReadSnapshot?(snapshotId: string): Promise<void>;
  GetRuntimeStateSnapshot?(): Promise<import("./runtimeStateStore").RuntimeProjection>;
  SyncRuntimeState?(): Promise<import("./runtimeStateStore").RuntimeProjection>;
  GetProjectTreeSnapshot(): Promise<ProjectTreeSnapshot>;
  GetProjectTreeRuntimeSnapshot?(): Promise<ProjectTreeRuntimeSnapshot>;
  ListProjectTopics(req: ProjectTopicPageRequest): Promise<ProjectTopicPage>;
  GetTopicSummary(key: ProjectTopicKey): Promise<ProjectNode>;
  GetSessionCatalogStatus(): Promise<SessionCatalogStatus>;
  RebuildSessionCatalog(): Promise<void>;
}

export interface ProjectTreeOrganizationBindings {
  GetSessionOrganization?(workspace: import("../generated/desktopContract.generated").SessionOrganizationWorkspace): Promise<import("../generated/desktopContract.generated").SessionOrganizationSnapshot>;
  UpdateSessionOrganization?(workspace: import("../generated/desktopContract.generated").SessionOrganizationWorkspace, expectedRevision: number,
    mutation: import("../generated/desktopContract.generated").SessionOrganizationMutation): Promise<import("../generated/desktopContract.generated").SessionOrganizationSnapshot>;
  ReorderTopics(scope: string, workspaceRoot: string, orderedTopicIDs: string[]): Promise<void>;
  ReorderSessions?(scope: string, workspaceRoot: string, orderedSessionKeys: string[]): Promise<void>;
  ListProjectGroups(scope: string, workspaceRoot: string): Promise<SessionGroup[]>;
  SaveSessionGroups(scope: string, workspaceRoot: string, groups: SessionGroup[]): Promise<void>;
  GetProjectGroups?(scope: string, workspaceRoot: string): Promise<ProjectGroupsSnapshot>;
  SaveSessionGroupsVersioned?(
    scope: string,
    workspaceRoot: string,
    expectedRevision: number,
    groups: SessionGroup[],
  ): Promise<ProjectGroupsSnapshot>;
}

// SessionReference is a session selected via @ past:chats for context injection.
export interface SessionReference {
  path: string;
  title: string;
  preview?: string;
  turns?: number;
  turnsState?: "unknown" | "valid" | "corrupt" | string;
  createdAt?: number;
  lastActivityAt?: number;
}
