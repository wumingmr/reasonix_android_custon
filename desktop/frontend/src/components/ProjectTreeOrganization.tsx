import { useCallback, useEffect, useRef, useState, lazy, Suspense, type DragEvent, type HTMLAttributes, type ReactNode } from "react";
import { Archive, Clock, FolderMinus, Pencil } from "lucide-react";
import { app } from "../lib/bridge";
import { asArray } from "../lib/array";
import type { Translator } from "../lib/i18n";
import { isTopicNode, projectTreeTopicArchiveBlocked } from "../lib/projectTreeTopic";
import type { ProjectTreeRefresh } from "../lib/projectTreeArchive";
import type { ProjectNode, ProjectTreeOrganizationBindings, SessionGroup } from "../lib/types";
import { forgetProjectTreeWindowLimit, projectTreeListKey, projectTreeListShowsLoading, projectTreeWindowProjection, type ProjectTreeListPageState } from "../lib/projectTreeWindow";
import { projectSessionIdentity } from "../lib/projectSessionIdentity";
import { mutateSessionOrganization, projectNodeSelector } from "../lib/sessionOrganization";
import type { SessionOrganizationMutation } from "../generated/desktopContract.generated";
import { useToast } from "../lib/toast";
import { ContextMenu, contextMenuPointFromEvent, type ContextMenuItem, type ContextMenuPoint } from "./ContextMenu";

export type ProjectDropPosition = "before" | "after";

export const GLOBAL_PROJECT_ORDER_KEY = "__global__";
const TOPIC_DRAG_TYPE = "application/x-reasonix-topic-id";

function projectOrderKey(node: ProjectNode): string {
  if (node.kind === "global_folder") return GLOBAL_PROJECT_ORDER_KEY;
  if (node.kind === "project" && node.root) return node.root;
  return "";
}

export function projectTreeProjectRoots(nodes: ProjectNode[]): string[] {
  return nodes.map(projectOrderKey).filter((key) => key !== "");
}

export function reorderedProjectRoots(
  nodes: ProjectNode[],
  draggedRoot: string,
  targetRoot: string,
  position: ProjectDropPosition,
): string[] {
  const roots = projectTreeProjectRoots(nodes);
  if (draggedRoot === targetRoot || !roots.includes(draggedRoot) || !roots.includes(targetRoot)) return roots;
  const next = roots.filter((root) => root !== draggedRoot);
  const targetIndex = next.indexOf(targetRoot);
  if (targetIndex < 0) return roots;
  next.splice(position === "before" ? targetIndex : targetIndex + 1, 0, draggedRoot);
  return next;
}

export function applyProjectOrder(nodes: ProjectNode[], roots: string[]): ProjectNode[] {
  const entries = nodes.map((node): [string, ProjectNode] => [projectOrderKey(node), node]).filter(([key]) => key !== "");
  const byRoot = new Map(entries);
  const ordered = roots.map((root) => byRoot.get(root)).filter((node): node is ProjectNode => Boolean(node));
  const orderedKeys = new Set(roots);
  return [...nodes.filter((node) => !orderedKeys.has(projectOrderKey(node))), ...ordered];
}

export function manualTopicOrder(a: ProjectNode, b: ProjectNode): number {
  const aOrder = typeof a.sortOrder === "number" && a.sortOrder >= 0 ? a.sortOrder : Number.MAX_SAFE_INTEGER;
  const bOrder = typeof b.sortOrder === "number" && b.sortOrder >= 0 ? b.sortOrder : Number.MAX_SAFE_INTEGER;
  return aOrder === bOrder ? 0 : aOrder - bOrder;
}

export function projectTreeOrganizationKey(node: ProjectNode): string {
  const remote = node.remote ?? node.remoteSession;
  if (remote) return `remote|${JSON.stringify([remote.hostId, remote.workspace])}`;
  return node.kind === "global_folder" || node.kind === "global_topic" ? "global|" : `project|${node.root ?? ""}`;
}

function splitOrganizationKey(key: string): { scope: "global" | "project"; root: string; hostId?: string } {
  if (key.startsWith("remote|")) { const [hostId, root] = JSON.parse(key.slice(7)) as [string,string]; return { scope: "project", root, hostId }; }
  return key === "global|" ? { scope: "global", root: "" } : { scope: "project", root: key.slice("project|".length) };
}

export function reorderedTopicIDs(
  nodes: ProjectNode[],
  scope: "global" | "project",
  root: string,
  draggedID: string,
  targetID: string,
  position: ProjectDropPosition,
): string[] | null {
  const parent = nodes.find((node) => projectTreeOrganizationKey(node) === (scope === "global" ? "global|" : `project|${root}`));
  if (!parent) return null;
  const ids = asArray(parent.children)
    .filter((node) => isTopicNode(node) && !node.runtimeOnly && node.topicId)
    .map((node) => node.topicId as string);
  if (draggedID === targetID || !ids.includes(draggedID) || !ids.includes(targetID)) return null;
  const rest = ids.filter((id) => id !== draggedID);
  const targetIndex = rest.indexOf(targetID);
  const insertAt = position === "before" ? targetIndex : targetIndex + 1;
  return [...rest.slice(0, insertAt), draggedID, ...rest.slice(insertAt)];
}

export function reorderedSessionKeys(
  nodes: ProjectNode[],
  scope: "global" | "project",
  root: string,
  draggedKey: string,
  targetKey: string,
  position: ProjectDropPosition,
): string[] | null {
  const parent = nodes.find((node) => projectTreeOrganizationKey(node) === (scope === "global" ? "global|" : `project|${root}`));
  if (!parent) return null;
  const keys = asArray(parent.children)
    .filter((node) => isTopicNode(node) && !node.runtimeOnly && (node.session || node.sessionPath))
    .map(projectSessionIdentity);
  if (draggedKey === targetKey || !keys.includes(draggedKey) || !keys.includes(targetKey)) return null;
  const rest = keys.filter((key) => key !== draggedKey);
  const targetIndex = rest.indexOf(targetKey);
  const insertAt = position === "before" ? targetIndex : targetIndex + 1;
  return [...rest.slice(0, insertAt), draggedKey, ...rest.slice(insertAt)];
}

export function projectTreeGroupContainsNode(group: SessionGroup, node: ProjectNode): boolean {
  const key = projectSessionIdentity(node);
  if (group.excludedSessionKeys?.includes(key)) return false;
  if (group.sessionKeys?.includes(key)) return true;
  return Boolean(node.topicId && group.topicIds?.includes(node.topicId));
}

function removeNodeFromGroup(group: SessionGroup, node: ProjectNode): SessionGroup {
  const key = projectSessionIdentity(node);
  const inheritsTopic = Boolean(node.topicId && group.topicIds?.includes(node.topicId));
  const excluded = new Set(group.excludedSessionKeys ?? []);
  if (inheritsTopic) excluded.add(key); else excluded.delete(key);
  return {
    ...group,
    sessionKeys: (group.sessionKeys ?? []).filter((candidate) => candidate !== key),
    excludedSessionKeys: [...excluded],
  };
}

function moveNodeToGroup(groups: SessionGroup[], node: ProjectNode, groupID: string): SessionGroup[] {
  const key = projectSessionIdentity(node);
  return groups.map((group) => {
    const without = removeNodeFromGroup(group, node);
    if (group.id !== groupID) return without;
    return {
      ...without,
      sessionKeys: [...(without.sessionKeys ?? []).filter((candidate) => candidate !== key), key],
      excludedSessionKeys: (without.excludedSessionKeys ?? []).filter((candidate) => candidate !== key),
    };
  });
}

type TopicRowDragProps = Pick<HTMLAttributes<HTMLDivElement>, "draggable" | "onDragStart" | "onDragOver" | "onDragLeave" | "onDrop" | "onDragEnd">;

export interface ProjectTreeOrganizationController {
  orderFor?(folder: ProjectNode): readonly string[];
  resetOrderMenuItems(folder: ProjectNode, t: Translator, closeMenu: () => void): ContextMenuItem[];
  topicRow(node: ProjectNode, disabled: boolean): { className: string; props: TopicRowDragProps };
  topicMenuItems(node: ProjectNode, t: Translator): ContextMenuItem[];
  createGroup(folder: ProjectNode, title: string): void;
  groupsFor(folder: ProjectNode): SessionGroup[];
  groupCollapsed(key: string, id: string): boolean;
  toggleGroup(key: string, id: string): void;
  renameGroup(key: string, id: string, title: string): void;
  deleteGroup(key: string, id: string): void;
  canDropTopicInto(key: string): boolean;
  dropTopicInto(key: string, groupID: string): void;
}

export function useProjectTreeOrganization({
  tree,
  refresh,
  onTopicsChanged,
  organizationRevision = 0,
  sortMode,
  bindings = app,
}: {
  tree: ProjectNode[];
  refresh: ProjectTreeRefresh;
  onTopicsChanged?: () => Promise<void> | void;
  organizationRevision?: number;
  // The activity order on screen; a first manual move starts from it.
  sortMode?: string;
  bindings?: ProjectTreeOrganizationBindings;
}): ProjectTreeOrganizationController {
  const { showToast } = useToast();
  const sortModeRef = useRef(sortMode);
  sortModeRef.current = sortMode;
  const [ordersByKey, setOrdersByKey] = useState<Record<string, string[]>>({});
  const [dragTopicID, setDragTopicID] = useState<string | null>(null);
  const [dropTopic, setDropTopic] = useState<{ topicID: string; position: ProjectDropPosition } | null>(null);
  const dragContextRef = useRef<{ scope: "global" | "project"; root: string; hostId?: string; key?: string } | null>(null);
  const [groupsByKey, setGroupsByKey] = useState<Record<string, SessionGroup[]>>({});
  const groupsRef = useRef(groupsByKey);
  const mountedRef = useRef(false);
  const loadedGroupsRef = useRef(new Set<string>());
  const loadingGroupsRef = useRef(new Set<string>());
  const groupMutationVersionsRef = useRef<Record<string, number>>({});
  const groupLoadSequencesRef = useRef<Record<string, number>>({});
  const groupSaveChainsRef = useRef(new Map<string, Promise<void>>());
  const organizationRevisionRef = useRef(organizationRevision);
  const [collapsedGroups, setCollapsedGroups] = useState(new Set<string>());

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const setKeyGroups = useCallback((key: string, groups: SessionGroup[]) => {
    groupsRef.current = { ...groupsRef.current, [key]: groups };
    setGroupsByKey(groupsRef.current);
  }, []);

  const loadGroups = useCallback((key: string, force = false) => {
    if (!force && (loadedGroupsRef.current.has(key) || loadingGroupsRef.current.has(key))) return;
    const sequence = (groupLoadSequencesRef.current[key] ?? 0) + 1;
    groupLoadSequencesRef.current[key] = sequence;
    const mutationVersion = groupMutationVersionsRef.current[key] ?? 0;
    loadingGroupsRef.current.add(key);
    const { scope, root, hostId } = splitOrganizationKey(key);
    const read = bindings.GetSessionOrganization ? bindings.GetSessionOrganization({ scope, workspaceRoot: root, hostId })
      : typeof bindings.GetProjectGroups === "function"
      ? bindings.GetProjectGroups(scope, root)
      : bindings.ListProjectGroups(scope, root).then((groups) => ({ groups, revision: 0, applied: true }));
    void read.then((snapshot) => {
      if (!mountedRef.current || groupLoadSequencesRef.current[key] !== sequence) return;
      if ((groupMutationVersionsRef.current[key] ?? 0) !== mutationVersion) return;
      // Never replace an optimistic state while its semantic mutations are
      // queued. The CAS path reads the newest server snapshot before applying.
      if (groupSaveChainsRef.current.has(key)) return;
      loadedGroupsRef.current.add(key);
      setKeyGroups(key, asArray(snapshot.groups));
      if ("order" in snapshot && "manualOrderEnabled" in snapshot) setOrdersByKey(current => ({ ...current, [key]: snapshot.manualOrderEnabled ? asArray(snapshot.order as string[]) : [] }));
    }).catch(() => {}).finally(() => {
      if (groupLoadSequencesRef.current[key] === sequence) loadingGroupsRef.current.delete(key);
    });
  }, [bindings, setKeyGroups]);

  useEffect(() => {
    const force = organizationRevisionRef.current !== organizationRevision;
    organizationRevisionRef.current = organizationRevision;
    for (const folder of tree) {
      if (folder.kind !== "project" && folder.kind !== "global_folder") continue;
      const key = projectTreeOrganizationKey(folder);
      loadGroups(key, force);
    }
  }, [loadGroups, organizationRevision, tree]);

  const mutateGroups = useCallback((key: string, update: (groups: SessionGroup[]) => SessionGroup[], mutation: SessionOrganizationMutation) => {
    const next = update(groupsRef.current[key] ?? []);
    groupMutationVersionsRef.current[key] = (groupMutationVersionsRef.current[key] ?? 0) + 1;
    loadedGroupsRef.current.add(key);
    setKeyGroups(key, next);
    if (!bindings.UpdateSessionOrganization) {
      showToast("Session organization is unavailable. Upgrade the desktop service.", "error");
      loadGroups(key, true);
      return;
    }
    const { scope, root, hostId } = splitOrganizationKey(key);
    const previous = groupSaveChainsRef.current.get(key) ?? Promise.resolve();
    const pending = previous.catch(() => {}).then(async () => {
      const saved = await mutateSessionOrganization(bindings, { scope, workspaceRoot: root, hostId }, mutation);
      if (mountedRef.current) {
        setKeyGroups(key, asArray(saved.groups));
        setOrdersByKey(current => ({ ...current, [key]: saved.manualOrderEnabled ? asArray(saved.order) : [] }));
      }
      await refresh({ reloadAllTopics: true });
    });
    groupSaveChainsRef.current.set(key, pending);
    void pending.catch(error => {
      if (groupSaveChainsRef.current.get(key) === pending) groupSaveChainsRef.current.delete(key);
      showToast(error instanceof Error ? error.message : String(error), "error");
      loadGroups(key, true);
    }).finally(() => {
      if (groupSaveChainsRef.current.get(key) === pending) groupSaveChainsRef.current.delete(key);
    });
  }, [bindings, loadGroups, refresh, setKeyGroups, showToast]);

  const clearTopicDrag = useCallback(() => {
    dragContextRef.current = null;
    setDragTopicID(null);
    setDropTopic(null);
  }, []);

  useEffect(() => {
    if (!dragTopicID) return;
    window.addEventListener("dragend", clearTopicDrag);
    window.addEventListener("drop", clearTopicDrag);
    window.addEventListener("blur", clearTopicDrag);
    return () => {
      window.removeEventListener("dragend", clearTopicDrag);
      window.removeEventListener("drop", clearTopicDrag);
      window.removeEventListener("blur", clearTopicDrag);
    };
  }, [clearTopicDrag, dragTopicID]);

  const topicRow = useCallback((node: ProjectNode, disabled: boolean) => {
    const topicID = node.topicId ?? "";
    const sessionKey = projectSessionIdentity(node);
    const key = projectTreeOrganizationKey(node);
    const draggable = !disabled && !node.runtimeOnly && topicID !== "" && Boolean(node.session || node.sessionPath);
    const sameScope = dragContextRef.current?.key === key;
    const className = sameScope && dropTopic?.topicID === sessionKey && dragTopicID !== sessionKey ? ` project-tree__topic--drop-${dropTopic.position}` : "";
    const props: TopicRowDragProps = { draggable };
    if (!draggable) return { className, props };
    props.onDragStart = (event) => {
      const context = splitOrganizationKey(key);
      dragContextRef.current = { ...context, key };
      event.dataTransfer.setData(TOPIC_DRAG_TYPE, sessionKey);
      event.dataTransfer.setData("text/plain", sessionKey);
      event.dataTransfer.effectAllowed = "move";
      setDragTopicID(sessionKey);
    };
    props.onDragOver = (event) => {
      if (!sameScope || !dragTopicID || dragTopicID === sessionKey) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
      const rect = event.currentTarget.getBoundingClientRect();
      const position = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
      setDropTopic((current) => current?.topicID === sessionKey && current.position === position ? current : { topicID: sessionKey, position });
    };
    props.onDragLeave = () => setDropTopic((current) => current?.topicID === sessionKey ? null : current);
    props.onDrop = (event: DragEvent<HTMLDivElement>) => {
      event.preventDefault();
      const draggedID = event.dataTransfer.getData(TOPIC_DRAG_TYPE) || dragTopicID;
      const context = dragContextRef.current;
      if (draggedID && context && sameScope) {
        const rect = event.currentTarget.getBoundingClientRect();
        const position = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
        const folder = tree.find(folder => projectTreeOrganizationKey(folder) === key);
        const dragged = folder?.children?.find(row => projectSessionIdentity(row) === draggedID);
        const targetGroup = (groupsRef.current[key] ?? []).find((group) => projectTreeGroupContainsNode(group, node));
        if (dragged && targetGroup && !projectTreeGroupContainsNode(targetGroup, dragged)) {
          mutateGroups(key, (groups) => moveNodeToGroup(groups, dragged, targetGroup.id), { kind: "set-group", target: projectNodeSelector(dragged), groupId: targetGroup.id });
        } else if (bindings.UpdateSessionOrganization && dragged) {
          void mutateSessionOrganization(bindings, { scope: context.scope, workspaceRoot: context.root, hostId: splitOrganizationKey(key).hostId },
            { kind: "move", target: projectNodeSelector(dragged), anchor: projectNodeSelector(node), position, sortMode: sortModeRef.current })
            .then(saved => { setOrdersByKey(current => ({ ...current, [key]: saved.order })); return refresh({ reloadAllTopics: true }); })
            .then(() => onTopicsChanged?.()).catch(error => { showToast(String(error), "error"); loadGroups(key, true); return refresh({ reloadAllTopics: true }); });
        } else {
          showToast("Session organization is unavailable. Upgrade the desktop service.", "error");
        }
      }
      clearTopicDrag();
    };
    props.onDragEnd = clearTopicDrag;
    return { className, props };
  }, [bindings, clearTopicDrag, dragTopicID, dropTopic, loadGroups, mutateGroups, onTopicsChanged, refresh, showToast, tree]);

  const removeTopicFromGroups = useCallback((node: ProjectNode) => {
    const key = projectTreeOrganizationKey(node);
    mutateGroups(key, (groups) => groups.map((group) => removeNodeFromGroup(group, node)), { kind: "set-group", target: projectNodeSelector(node), groupId: "" });
  }, [mutateGroups]);

  const forgetGroupWindow = useCallback((key: string, id: string) => {
    const folder = tree.find((node) => projectTreeOrganizationKey(node) === key);
    if (folder) forgetProjectTreeWindowLimit(projectTreeListKey(folder.key, id));
  }, [tree]);

  return {
    orderFor(folder) { return ordersByKey[projectTreeOrganizationKey(folder)] ?? []; },
    resetOrderMenuItems(folder, t, closeMenu) {
      const key = projectTreeOrganizationKey(folder);
      return (ordersByKey[key]?.length ?? 0) > 0 ? [{
        key: "reset-manual-order", icon: <Clock size={13} />, label: t("projectTree.resetManualOrder"),
        onSelect: () => { closeMenu(); mutateGroups(key, (groups) => groups, { kind: "reset-order" }); },
      }] : [];
    },
    topicRow,
    topicMenuItems(node, t) {
      if (!(groupsRef.current[projectTreeOrganizationKey(node)] ?? []).some((group) => projectTreeGroupContainsNode(group, node))) return [];
      return [{ key: "remove-from-group", icon: <FolderMinus size={13} />, label: t("projectTree.removeFromGroup"), onSelect: () => removeTopicFromGroups(node) }];
    },
    createGroup(folder, title) {
      const key = projectTreeOrganizationKey(folder);
      const suffix = typeof crypto !== "undefined" && crypto.randomUUID ? crypto.randomUUID() : `${Date.now()}-${Math.random().toString(16).slice(2)}`;
      mutateGroups(key, (groups) => [...groups, { id: `group-${suffix}`, title, topicIds: [] }], { kind: "create-group", groupId: `group-${suffix}`, title });
    },
    groupsFor(folder) { return groupsByKey[projectTreeOrganizationKey(folder)] ?? []; },
    groupCollapsed(key, id) { return collapsedGroups.has(`${key}|${id}`); },
    toggleGroup(key, id) {
      setCollapsedGroups((current) => {
        const next = new Set(current), collapseKey = `${key}|${id}`;
        if (next.has(collapseKey)) next.delete(collapseKey); else next.add(collapseKey);
        return next;
      });
    },
    renameGroup(key, id, title) {
      const trimmed = title.trim();
      if (!trimmed) forgetGroupWindow(key, id);
      mutateGroups(key, (groups) => trimmed ? groups.map((group) => group.id === id ? { ...group, title: trimmed } : group) : groups.filter((group) => group.id !== id), { kind: trimmed ? "rename-group" : "delete-group", groupId: id, title: trimmed });
    },
    deleteGroup(key, id) {
      forgetGroupWindow(key, id);
      mutateGroups(key, (groups) => groups.filter((group) => group.id !== id), { kind: "delete-group", groupId: id });
    },
    canDropTopicInto(key) {
      const context = dragContextRef.current;
      return Boolean(dragTopicID && context?.key === key);
    },
    dropTopicInto(key, groupID) {
      const sessionKey = dragTopicID;
      if (!sessionKey) return;
      const folder = tree.find((candidate) => projectTreeOrganizationKey(candidate) === key);
      const node = asArray(folder?.children).find((candidate) => projectSessionIdentity(candidate) === sessionKey);
      if (!node) return;
      mutateGroups(key, (groups) => moveNodeToGroup(groups, node, groupID), { kind: "set-group", target: projectNodeSelector(node), groupId: groupID });
      clearTopicDrag();
    },
  };
}

export function ProjectTreeGroupRows({
  folder,
  children,
  depth,
  section,
  visible,
  organization,
  renderNode,
  t,
  queryActive,
  remote,
  activeTopicId,
  isActive,
  listState,
  listLimit,
  onEnsureList,
  onExpandList,
  onRetryList,
  onForgetList,
}: {
  folder: ProjectNode;
  children: ProjectNode[];
  depth: number;
  section: "pinned" | "projects";
  visible: boolean;
  organization: ProjectTreeOrganizationController;
  renderNode: (node: ProjectNode, depth: number, section: "pinned" | "projects", visible: boolean) => ReactNode;
  t: Translator;
  queryActive: boolean;
  remote: boolean;
  activeTopicId?: string;
  isActive: (node: ProjectNode) => boolean;
  listState: (groupID: string) => ProjectTreeListPageState | undefined;
  listLimit: (groupID: string) => number;
  onEnsureList: (groupID: string) => void;
  onExpandList: (groupID: string, loadedCount: number) => void;
  onRetryList: (groupID: string) => void;
  onForgetList: (groupID: string) => void;
}) {
  const [menuGroup, setMenuGroup] = useState<string | null>(null);
  const [menuPoint, setMenuPoint] = useState<ContextMenuPoint | null>(null);
  const [editingGroup, setEditingGroup] = useState<string | null>(null);
  const [groupDraft, setGroupDraft] = useState("");
  const key = projectTreeOrganizationKey(folder);
  const groups = organization.groupsFor(folder);
  const groupIDs = groups.map((group) => group.id).join("\u001f");
  const expandedGroupIDs = groups
    .filter((group) => !organization.groupCollapsed(key, group.id))
    .map((group) => group.id)
    .join("\u001f");
  const previousActiveTopicRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    const previous = previousActiveTopicRef.current;
    previousActiveTopicRef.current = activeTopicId;
    if (!activeTopicId || previous === activeTopicId) return;
    const activeNode = children.find(isActive);
    const activeGroup = activeNode ? groups.find((group) => projectTreeGroupContainsNode(group, activeNode)) : undefined;
    if (activeGroup && organization.groupCollapsed(key, activeGroup.id)) organization.toggleGroup(key, activeGroup.id);
  }, [activeTopicId, children, groupIDs, groups, isActive, key, organization]);
  useEffect(() => {
    if (!visible || queryActive || remote) return;
    onEnsureList("");
    for (const groupID of expandedGroupIDs.split("\u001f")) {
      if (groupID) onEnsureList(groupID);
    }
  }, [expandedGroupIDs, onEnsureList, queryActive, remote, visible]);

  const renderWindowControls = (groupID: string, label: string, loadedCount: number, hasHiddenLoadedRows: boolean) => {
    if (queryActive) return null;
    const state = listState(groupID);
    const showLoading = projectTreeListShowsLoading(state);
    const canExpand = hasHiddenLoadedRows || Boolean(state?.nextCursor);
    if (!showLoading && !state?.error && !canExpand) return null;
    return <div className="project-tree__topic-window-actions" style={{ paddingLeft: 14 + depth * 16 }}>
      {state?.error ? <button type="button" className="project-tree__topic-window-toggle" aria-label={t("projectTree.retryGroup", { name: label })} onClick={() => onRetryList(groupID)}>
        {t("projectTree.loadFailedRetry")}
      </button> : null}
      {showLoading ? <span className="project-tree__topic-window-status">{t("projectTree.loadingMore")}</span> : null}
      {!showLoading && !state?.error && canExpand ? <button type="button" className="project-tree__topic-window-toggle" disabled={state?.loading} aria-label={t("projectTree.expandGroup", { name: label })} onClick={() => onExpandList(groupID, loadedCount)}>
        {t("projectTree.expandDisplay")}
      </button> : null}
    </div>;
  };

  const scopedRows = (groupID: string, members: ProjectNode[]) => {
    if (remote) {
      const order = organization.orderFor?.(folder) ?? [];
      const ranks = new Map(order.map((key, index) => [key, index]));
      return [...members].sort((a,b) => (ranks.get(projectSessionIdentity(a)) ?? Number.MAX_SAFE_INTEGER) - (ranks.get(projectSessionIdentity(b)) ?? Number.MAX_SAFE_INTEGER));
    }
    const state = listState(queryActive ? "" : groupID);
    const accepted = new Set(state?.itemKeys ?? []);
    const rows = members.filter((member) => accepted.has(member.key));
    if (!queryActive) {
      const active = members.find(isActive);
      if (active && !rows.some((row) => row.key === active.key)) rows.push(active);
    }
    return rows;
  };

  const ungrouped = children.filter((child) => !groups.some((group) => projectTreeGroupContainsNode(group, child)));
  const ungroupedRows = scopedRows("", ungrouped);
  const ungroupedProjection = queryActive
    ? { rows: ungroupedRows, hasHiddenLoadedRows: false }
    : projectTreeWindowProjection(ungroupedRows, listLimit(""), isActive);
  const commitRename = (id: string) => {
    if (!groupDraft.trim()) onForgetList(id);
    organization.renameGroup(key, id, groupDraft);
    setEditingGroup(null);
  };
  return <>
    {ungroupedProjection.rows.map((child) => renderNode(child, depth, section, visible))}
    {renderWindowControls("", folder.label, ungroupedRows.length, ungroupedProjection.hasHiddenLoadedRows)}
    {groups.map((group) => {
      const collapsed = queryActive ? false : organization.groupCollapsed(key, group.id);
      const members = children.filter((child) => projectTreeGroupContainsNode(group, child));
      const groupRows = scopedRows(group.id, members);
      const groupProjection = queryActive
        ? { rows: groupRows, hasHiddenLoadedRows: false }
        : projectTreeWindowProjection(groupRows, listLimit(group.id), isActive);
      if (queryActive && groupRows.length === 0) return null;
      const canDrop = organization.canDropTopicInto(key);
      return <div key={group.id} className={`project-tree__group${collapsed ? " project-tree__group--collapsed" : ""}`}>
        <div
          role="button"
          tabIndex={0}
          className={`project-tree__group-main${canDrop ? " project-tree__group-main--drop-target" : ""}`}
          style={{ paddingLeft: 14 + depth * 16 }}
          title={group.title}
          onClick={() => organization.toggleGroup(key, group.id)}
          onKeyDown={(event) => {
            if (editingGroup === group.id) return;
            if (event.key === "Enter" || event.key === " ") organization.toggleGroup(key, group.id);
          }}
          onContextMenu={(event) => {
            event.preventDefault();
            setMenuGroup(group.id);
            setMenuPoint(contextMenuPointFromEvent(event));
          }}
          onDragOver={(event) => {
            if (!canDrop) return;
            event.preventDefault();
            event.dataTransfer.dropEffect = "move";
          }}
          onDrop={(event) => {
            event.preventDefault();
            if (canDrop) organization.dropTopicInto(key, group.id);
          }}
        >
          <span className="project-tree__group-chevron" aria-hidden="true">{collapsed ? "▸" : "▾"}</span>
          {editingGroup === group.id ? <input
            autoFocus
            className="project-tree__group-input"
            value={groupDraft}
            onChange={(event) => setGroupDraft(event.target.value)}
            onFocus={(event) => event.target.select()}
            onKeyDown={(event) => {
              if (event.key === "Enter") commitRename(group.id);
              if (event.key === "Escape") setEditingGroup(null);
            }}
            onBlur={() => commitRename(group.id)}
            onClick={(event) => event.stopPropagation()}
          /> : <span className="project-tree__group-title">{group.title}</span>}
        </div>
        {menuGroup === group.id && <ContextMenu
          open
          point={menuPoint}
          items={[
            { key: "rename", icon: <Pencil size={13} />, label: t("projectTree.renameGroup"), onSelect: () => { setEditingGroup(group.id); setGroupDraft(group.title); setMenuGroup(null); } },
            { key: "delete", icon: <Archive size={13} />, label: t("projectTree.deleteGroup"), danger: true, onSelect: () => { onForgetList(group.id); organization.deleteGroup(key, group.id); setMenuGroup(null); } },
          ]}
          minWidth={178}
          ariaLabel={t("projectTree.renameGroup")}
          onClose={() => setMenuGroup(null)}
        />}
        {!collapsed && <div className="project-tree__group-children">
          {groupProjection.rows.map((child) => renderNode(child, depth, section, visible))}
          {renderWindowControls(group.id, group.title, groupRows.length, groupProjection.hasHiddenLoadedRows)}
        </div>}
      </div>;
    })}
  </>;
}

export function projectTreeFolderHasActiveRuntime(folder: ProjectNode): boolean {
  return asArray(folder.children).some(projectTreeTopicArchiveBlocked);
}

const FolderActivity = lazy(() => import("./RuntimeActivityIndicator"));
export function ProjectTreeFolderActivity({ folder }: { folder: ProjectNode }) {
  return <Suspense fallback={null}><FolderActivity target={{
    scope: folder.kind === "global_folder" ? "global" : "project",
    root: folder.root ?? "",
    remote: folder.remote,
    topics: asArray(folder.children),
    fallbackActive: projectTreeFolderHasActiveRuntime(folder),
  }} /></Suspense>;
}
