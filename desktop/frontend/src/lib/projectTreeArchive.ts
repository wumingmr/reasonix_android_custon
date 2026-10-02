import { useCallback, useRef, useState } from "react";
import { app } from "./bridge";
import { projectTreeFolderKeyForSession, projectTreeFolderKeyForTopic } from "./projectTreeTopic";
import type { ToastContextValue } from "./toast";
import type { ProjectNode } from "./types";
import { sessionLifecycleFences } from "./sessionLifecycleFences";
import { projectSessionIdentity } from "./projectSessionIdentity";
import { useT } from "./i18n";
import type { TopicRemovalInspection, TopicRemovalRequest } from "../generated/desktopContract.generated";

export { projectTreeWithoutTopics } from "./projectTreeTopic";

export type ProjectTreeRefreshOptions = {
  reloadTopicKeys?: string[];
  reloadAllTopics?: boolean;
  onReloadStarted?: () => void;
};

export type ProjectTreeRefresh = (options?: ProjectTreeRefreshOptions) => Promise<void>;

export async function reloadProjectTreeTopics(
  projects: ProjectNode[],
  options: ProjectTreeRefreshOptions | undefined,
  load: (project: ProjectNode) => Promise<void>,
): Promise<void> {
  const keys = new Set(options?.reloadTopicKeys ?? []);
  const targets = projects.filter((project) => options?.reloadAllTopics || keys.has(project.key));
  const pendingLoads = targets.map(load);
  if (pendingLoads.length > 0) options?.onReloadStarted?.();
  await Promise.all(pendingLoads);
}

export function enqueueProjectTreeArchive(previous: Promise<void>, work: () => Promise<void>): Promise<void> {
  return previous.catch(() => undefined).then(work);
}

export function projectTreeTopicArchiveTargetKey(
  scope: "global" | "project",
  workspaceRoot: string,
  topicId: string,
): string {
  return JSON.stringify(["topic", scope, workspaceRoot.trim(), topicId.trim()]);
}

export function projectTreeSessionArchiveTargetKey(sessionPath: string): string {
  return JSON.stringify(["session", sessionPath.trim()]);
}

export async function runProjectTreeArchiveJob({
  archive,
  commit,
  reload,
  finishPending,
  recover,
}: {
  archive: () => Promise<void | boolean>;
  commit: () => void;
  reload: () => Promise<void>;
  finishPending: () => void;
  recover: (error: unknown) => Promise<void>;
}): Promise<boolean> {
  try {
    if (await archive() === false) { finishPending(); return false; }
  } catch (error) {
    // Failed mutations must become visible to the recovery reload.
    finishPending();
    await recover(error);
    return false;
  }
  try {
    // A tombstone is a post-commit stale-response fence, not an optimistic
    // archive. Installing it only after backend success keeps rejected topics
    // visible throughout the mutation and its recovery reload.
    commit();
    // Keep the visible pending state active until the canonical folder page
    // has landed. The caller may release its stale-response tombstone once
    // that reload has acquired a newer request generation.
    await reload();
    return true;
  } finally {
    finishPending();
  }
}

export function projectTreeTrashingTopics(previous: Set<string>, topicId: string, trashing: boolean): Set<string> {
  const id = topicId.trim();
  if (!id || previous.has(id) === trashing) return previous;
  const next = new Set(previous);
  if (trashing) next.add(id);
  else next.delete(id);
  return next;
}

export async function archiveProjectTreeSession({
  sessionPath,
  archiveTarget,
  refresh,
  topicsChanged,
  showError,
}: {
  sessionPath: string;
  archiveTarget: (selector: { sessionPath: string }) => Promise<unknown>;
  refresh: () => Promise<void>;
  topicsChanged?: () => Promise<void> | void;
  showError: (error: unknown) => void;
}): Promise<boolean> {
  try {
    await archiveTarget({ sessionPath });
    await refresh();
    await Promise.resolve(topicsChanged?.()).catch(() => undefined);
    return true;
  } catch (error) {
    showError(error);
    await refresh().catch(() => undefined);
    return false;
  }
}

export function useProjectTreeArchiveState() {
  const topicsRef = useRef<Set<string>>(new Set());
  const tombstonesRef = useRef<Set<string>>(new Set());
  const [topics, setTopics] = useState<Set<string>>(new Set());
  const begin = useCallback((topicId: string) => {
    const id = topicId.trim();
    if (!id || topicsRef.current.has(id)) return false;
    topicsRef.current = projectTreeTrashingTopics(topicsRef.current, id, true);
    setTopics(topicsRef.current);
    return true;
  }, []);
  const commit = useCallback((topicId: string) => {
    tombstonesRef.current = projectTreeTrashingTopics(tombstonesRef.current, topicId, true);
  }, []);
  const end = useCallback((topicId: string) => {
    topicsRef.current = projectTreeTrashingTopics(topicsRef.current, topicId, false);
    tombstonesRef.current = projectTreeTrashingTopics(tombstonesRef.current, topicId, false);
    setTopics(topicsRef.current);
  }, []);
  const releaseTombstone = useCallback((topicId: string) => {
    tombstonesRef.current = projectTreeTrashingTopics(tombstonesRef.current, topicId, false);
  }, []);
  const currentTombstones = useCallback((): ReadonlySet<string> => new Set([...tombstonesRef.current, ...sessionLifecycleFences.keys()]), []);
  return {
    trashingTopics: topics,
    beginTrashingTopic: begin,
    commitArchiveTombstone: commit,
    endTrashingTopic: end,
    releaseArchiveTombstone: releaseTombstone,
    currentArchiveTombstones: currentTombstones,
  };
}

export function useProjectTreeArchiveController({
  treeRef,
  invalidateProjectTopicLists,
  refreshRef,
  optimisticallyRemoveTopic,
  optimisticallyRemoveSession,
  closeMenu,
  onTopicsChanged,
  showToast,
  sessionErrorMessage,
}: {
  treeRef: { current: ProjectNode[] };
  invalidateProjectTopicLists: (projectKey: string) => void;
  refreshRef: { current: ProjectTreeRefresh };
  optimisticallyRemoveTopic: (topicId: string) => void;
  optimisticallyRemoveSession: (node: ProjectNode) => void;
  closeMenu: () => void;
  onTopicsChanged?: () => Promise<void> | void;
  showToast: ToastContextValue["showToast"];
  sessionErrorMessage?: (error: unknown) => string;
}) {
  const t = useT();
  const [topicRemovalInspections, setTopicRemovalInspections] = useState<Record<string, TopicRemovalInspection>>({});
  const topicRemovalRequests = useRef(new Map<string, TopicRemovalRequest>());
  const inspectTopicRemoval = useCallback(async (topicId: string) => {
    const view = await app.InspectTopicRemoval({ workspaceId: "", topicId });
    setTopicRemovalInspections(previous => ({ ...previous, [topicId]: view }));
    return view;
  }, []);
  const {
    trashingTopics,
    beginTrashingTopic,
    commitArchiveTombstone,
    endTrashingTopic,
    releaseArchiveTombstone,
    currentArchiveTombstones,
  } = useProjectTreeArchiveState();
  const sessionTrashingRef = useRef<Set<string>>(new Set());
  const [trashingSessions, setTrashingSessions] = useState<Set<string>>(new Set());
  const archiveQueueRef = useRef<Promise<void>>(Promise.resolve());

  const trashTopic = useCallback(async (topicId: string) => {
    if (!beginTrashingTopic(topicId)) return;
    const folderKey = projectTreeFolderKeyForTopic(treeRef.current, topicId);
    const reloadOptions: ProjectTreeRefreshOptions = {
      reloadTopicKeys: folderKey ? [folderKey] : undefined,
      reloadAllTopics: !folderKey,
      onReloadStarted: () => releaseArchiveTombstone(topicId),
    };
    const invalidatedKeys = folderKey
      ? [folderKey]
      : treeRef.current.filter((node) => node.kind === "project" || node.kind === "global_folder").map((node) => node.key);
    closeMenu();

    const queued = enqueueProjectTreeArchive(archiveQueueRef.current, async () => {
      await runProjectTreeArchiveJob({
        archive: async () => (await import("./topicRemovalCommand")).removeProjectTopic(topicId, inspectTopicRemoval, topicRemovalRequests.current, t),
        commit: () => {
          commitArchiveTombstone(topicId);
          // Retire requests and their loading/cursor state together before
          // the post-commit page replaces the visible list.
          invalidatedKeys.forEach(invalidateProjectTopicLists);
          optimisticallyRemoveTopic(topicId);
        },
        reload: async () => {
          await refreshRef.current(reloadOptions);
          await Promise.resolve(onTopicsChanged?.()).catch(() => undefined);
        },
        finishPending: () => endTrashingTopic(topicId),
        recover: async (err) => {
          showToast(err instanceof Error ? err.message : String(err), "error");
          await refreshRef.current(reloadOptions);
        },
      });
    });
    archiveQueueRef.current = queued;
    await queued;
  }, [beginTrashingTopic, closeMenu, commitArchiveTombstone, endTrashingTopic, inspectTopicRemoval, invalidateProjectTopicLists, onTopicsChanged, optimisticallyRemoveTopic, refreshRef, releaseArchiveTombstone, showToast, t, treeRef]);

  const trashSession = useCallback(async (target: ProjectNode) => {
    const sessionPath = (target.sessionPath ?? "").trim();
    const targetKey = projectSessionIdentity(target);
    if (!sessionPath || sessionTrashingRef.current.has(targetKey)) return;
    const folderKey = projectTreeFolderKeyForSession(treeRef.current, sessionPath);
    const reloadOptions: ProjectTreeRefreshOptions = {
      reloadTopicKeys: folderKey ? [folderKey] : undefined,
      reloadAllTopics: !folderKey,
    };
    sessionTrashingRef.current = projectTreeTrashingTopics(sessionTrashingRef.current, targetKey, true);
    setTrashingSessions(sessionTrashingRef.current);
    closeMenu();

    const queued = enqueueProjectTreeArchive(archiveQueueRef.current, async () => {
      await runProjectTreeArchiveJob({
        archive: async () => {
          const receipt = await app.ArchiveSessionTarget({ ref: target.session, source: target.source, sessionPath });
          if (receipt.committed) sessionLifecycleFences.archive(target, receipt);
          if (receipt.committed && receipt.outcome === "archived_copy") showToast(t("projectTree.archivedCopy"), "info");
          if (receipt.committed && receipt.outcome === "already_removed") showToast(t("projectTree.alreadyRemovedSource"), "info");
        },
        commit: () => {
          const invalidatedKeys = folderKey ? [folderKey] : treeRef.current.filter((node) => node.kind === "project" || node.kind === "global_folder").map((node) => node.key);
          invalidatedKeys.forEach(invalidateProjectTopicLists);
          optimisticallyRemoveSession(target);
        },
        reload: async () => {
          await refreshRef.current(reloadOptions);
          await Promise.resolve(onTopicsChanged?.()).catch(() => undefined);
        },
        finishPending: () => {
          sessionTrashingRef.current = projectTreeTrashingTopics(sessionTrashingRef.current, targetKey, false);
          setTrashingSessions(sessionTrashingRef.current);
        },
        recover: async (err) => {
          showToast(sessionErrorMessage?.(err) ?? (err instanceof Error ? err.message : String(err)), "error");
          await refreshRef.current(reloadOptions);
        },
      });
    });
    archiveQueueRef.current = queued;
    await queued;
  }, [closeMenu, invalidateProjectTopicLists, onTopicsChanged, optimisticallyRemoveSession, refreshRef, sessionErrorMessage, showToast, t, treeRef]);

  return { trashingTopics, trashingSessions, currentArchiveTombstones, trashTopic, trashSession, inspectTopicRemoval, topicRemovalInspections };
}
