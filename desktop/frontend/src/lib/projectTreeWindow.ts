import type { ProjectNode } from "./types";
import { isStaleRead, releaseReadSnapshot } from "./readSnapshot";

export const PROJECT_TREE_WINDOW_INITIAL = 5;
export const PROJECT_TREE_WINDOW_STEP = 5;
export const PROJECT_TREE_SEARCH_PAGE = 50;
export const PROJECT_TREE_BACKEND_PAGE_MAX = 200;

export type ProjectTreeListPageState = {
  snapshotId?: string;
  itemKeys?: string[];
  nextCursor?: string;
  loading: boolean;
  // A pending background read with a resident page keeps the painted UI quiet.
  refreshing?: boolean;
  initialized?: boolean;
  error?: string;
};

export function projectTreeListShowsLoading(state: ProjectTreeListPageState | undefined): boolean {
  return Boolean(state?.loading && !state.refreshing);
}

const runtimeWindowLimits = new Map<string, number>();

export function projectTreeRuntimeWindowLimits(): Record<string, number> {
  return Object.fromEntries(runtimeWindowLimits);
}

export function rememberProjectTreeWindowLimit(key: string, limit: number): void {
  if (limit <= PROJECT_TREE_WINDOW_INITIAL) runtimeWindowLimits.delete(key);
  else runtimeWindowLimits.set(key, limit);
}

export function forgetProjectTreeWindowLimit(key: string): void {
  runtimeWindowLimits.delete(key);
}

export function forgetProjectTreeWindowLimits(projectKeys: ReadonlySet<string>): void {
  for (const key of runtimeWindowLimits.keys()) {
    const separator = key.indexOf("\u001f");
    const projectKey = separator >= 0 ? key.slice(0, separator) : key;
    if (!projectKeys.has(projectKey)) runtimeWindowLimits.delete(key);
  }
}

export function resetProjectTreeRuntimeWindowLimits(projectKey?: string): void {
  if (!projectKey) {
    runtimeWindowLimits.clear();
    return;
  }
  const prefix = `${projectKey}\u001f`;
  for (const key of runtimeWindowLimits.keys()) {
    if (key.startsWith(prefix)) runtimeWindowLimits.delete(key);
  }
}

export type ProjectTreeRequestLimiter = {
  run<T>(task: () => Promise<T>): Promise<T>;
  stats(): ProjectTreeRequestLimiterStats;
};

export type ProjectTreeRequestLimiterStats = {
  active: number;
  queued: number;
};

type ProjectTreePage<T> = {
  snapshotId?: string;
  replacedSnapshot?: boolean;
  items: T[];
  nextCursor?: string;
  revision: number;
  complete?: boolean;
};

export async function loadProjectTreePageWindow<T, TPage extends ProjectTreePage<T>>(
  initialCursor: string,
  requestedLimit: number,
  load: (cursor: string, limit: number) => Promise<TPage>,
  recoveryLimit = requestedLimit,
  allowRecovery = true,
): Promise<TPage & { replacedSnapshot?: boolean }> {
  const items: T[] = [];
  let cursor = initialCursor;
  let remaining = Math.max(1, Math.floor(requestedLimit));
  let result: TPage | undefined;
  let revision = 0;
  let incomplete = false;

  try {
  while (remaining > 0) {
    const page = await load(cursor, Math.min(remaining, PROJECT_TREE_BACKEND_PAGE_MAX));
    if (result?.snapshotId && page.snapshotId !== result.snapshotId) {
      releaseReadSnapshot(page.snapshotId);
      throw new Error("Mixed read snapshots in one list window");
    }
    if (page.nextCursor && (page.nextCursor === cursor || page.items.length === 0)) throw new Error("List cursor did not advance");
    result = page;
    items.push(...page.items);
    revision = Math.max(revision, page.revision);
    incomplete = incomplete || page.complete === false;
    remaining -= page.items.length;
    if (!page.nextCursor || page.items.length === 0) break;
    cursor = page.nextCursor;
  }
  } catch (error) {
    if (!initialCursor) releaseReadSnapshot(result?.snapshotId);
    if (!allowRecovery || !isStaleRead(error)) throw error;
    const rebuilt = await loadProjectTreePageWindow("", recoveryLimit, load, recoveryLimit, false);
    return { ...rebuilt, replacedSnapshot: true };
  }

  if (!result) throw new Error("project tree page loader returned no page");
  return {
    ...result,
    items,
    revision,
    complete: incomplete ? false : result.complete,
  };
}

export function createProjectTreeRequestLimiter(maxConcurrent = 4): ProjectTreeRequestLimiter {
  const limit = Math.max(1, Math.floor(maxConcurrent));
  let active = 0;
  const pending: Array<() => void> = [];

  const release = () => {
    active = Math.max(0, active - 1);
    pending.shift()?.();
  };

  return {
    run<T>(task: () => Promise<T>): Promise<T> {
      return new Promise<T>((resolve, reject) => {
        const start = () => {
          active += 1;
          void task().then(resolve, reject).finally(release);
        };
        if (active < limit) start();
        else pending.push(start);
      });
    },
    stats(): ProjectTreeRequestLimiterStats {
      return { active, queued: pending.length };
    },
  };
}

export function projectTreeListKey(projectKey: string, groupID = "", query = ""): string {
  const normalizedQuery = query.trim().toLowerCase();
  if (normalizedQuery) return `${projectKey}\u001fsearch\u001f${normalizedQuery}`;
  return `${projectKey}\u001f${groupID ? `group:${groupID}` : "ungrouped"}`;
}

export function projectTreeKnownGroupIDs(
  pageStates: Readonly<Record<string, ProjectTreeListPageState>>,
  projectKey: string,
): string[] {
  const prefix = `${projectKey}\u001fgroup:`;
  return [...new Set(Object.keys(pageStates)
    .filter((key) => key.startsWith(prefix))
    .map((key) => key.slice(prefix.length))
    .filter(Boolean))].sort();
}

export function projectTreeListNeedsInitialization(state: ProjectTreeListPageState | undefined): boolean {
  return !state?.initialized && !state?.loading;
}

export function projectTreeProjectsNeedingInitialLoad(
  projects: readonly ProjectNode[],
  expandedKeys: ReadonlySet<string>,
  query: string,
  pageStates: Readonly<Record<string, ProjectTreeListPageState>>,
  folderKey: (project: ProjectNode) => string,
): ProjectNode[] {
  return projects.filter((project) => (
    !project.remote
    && (project.kind === "project" || project.kind === "global_folder")
    && expandedKeys.has(folderKey(project))
    && projectTreeListNeedsInitialization(pageStates[projectTreeListKey(project.key, "", query)])
  ));
}

export async function reloadProjectTreeTopicLists(
  project: ProjectNode,
  query: string,
  pageStates: Readonly<Record<string, ProjectTreeListPageState>>,
  load: (project: ProjectNode, groupID: string) => Promise<void>,
): Promise<void> {
  const groupIDs = query.trim() ? [""] : ["", ...projectTreeKnownGroupIDs(pageStates, project.key)];
  await Promise.all(groupIDs.map((groupID) => load(project, groupID)));
}

export type ProjectTreeWindowProjection = {
  rows: ProjectNode[];
  hasHiddenLoadedRows: boolean;
};

export function projectTreeWindowProjection(
  rows: ProjectNode[],
  limit: number,
  isActive: (node: ProjectNode) => boolean,
): ProjectTreeWindowProjection {
  if (rows.length <= limit) return { rows, hasHiddenLoadedRows: false };
  const visible = rows.slice(0, limit);
  const active = rows.find((row) => isActive(row));
  const projected = !active || visible.some((row) => row.key === active.key)
    ? visible
    : [...visible, active];
  const visibleKeys = new Set(projected.map((row) => row.key));
  return {
    rows: projected,
    hasHiddenLoadedRows: rows.some((row) => !visibleKeys.has(row.key)),
  };
}

export function projectTreeWindowRows(
  rows: ProjectNode[],
  limit: number,
  isActive: (node: ProjectNode) => boolean,
): ProjectNode[] {
  return projectTreeWindowProjection(rows, limit, isActive).rows;
}
