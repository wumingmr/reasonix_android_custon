import { asArray } from "./array";
import { projectSessionRowKey } from "./projectSessionIdentity";
import { topicIsActive } from "./projectTreeTopic";
import type { ProjectNode } from "./types";

export function projectTreeNodeKey(node: ProjectNode, depth: number): string {
  if (node.session || node.sessionPath || node.source || node.remoteSession || node.tabId) return projectSessionRowKey(node);
  return node.key || `${node.kind}-${node.root ?? ""}-${node.topicId ?? ""}-${node.sessionPath ?? ""}-${depth}`;
}

export function collapsibleProjectTreeFolderKeys(nodes: ProjectNode[], depth = 0): string[] {
  const keys: string[] = [];
  for (const node of nodes) {
    if (!node) continue;
    const children = asArray(node.children);
    if ((node.kind === "project" || node.kind === "global_folder") && children.length > 0) keys.push(projectTreeNodeKey(node, depth));
    keys.push(...collapsibleProjectTreeFolderKeys(children, depth + 1));
  }
  return keys;
}

export function activeSessionAncestorKeys(
  nodes: ProjectNode[],
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): string[] {
  const walk = (nodeList: ProjectNode[], ancestors: string[]): string[] | null => {
    for (const node of nodeList) {
      if (!node) continue;
      if (topicIsActive(node, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath)) return ancestors;
      const children = asArray(node.children);
      if (children.length > 0) {
        const next = walk(children, [...ancestors, projectTreeNodeKey(node, ancestors.length)]);
        if (next) return next;
      }
    }
    return null;
  };
  const found = walk(nodes, []);
  if (found) return found;
  // Shell-only snapshots no longer embed topics. Expand the matching project or
  // Global folder by workspace identity so the first lazy page can load.
  const scope = (activeScope ?? "").trim();
  const root = (activeWorkspaceRoot ?? "").trim();
  for (const node of nodes) {
    if (!node) continue;
    if (scope === "global" && node.kind === "global_folder") return [projectTreeNodeKey(node, 0)];
    if (node.kind === "project" && root && (node.root === root || node.root === activeWorkspaceRoot)) return [projectTreeNodeKey(node, 0)];
    if (!scope && !root && activeTopicId && (node.kind === "project" || node.kind === "global_folder")) return [projectTreeNodeKey(node, 0)];
  }
  return [];
}

export function defaultExpandedProjectTreeKeys(
  nodes: ProjectNode[],
  activeScope?: string,
  activeWorkspaceRoot?: string,
  activeTopicId?: string,
  activeSessionPath?: string,
): string[] {
  const keys = activeSessionAncestorKeys(nodes, activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath);
  if (keys.length > 0 || [activeScope, activeWorkspaceRoot, activeTopicId, activeSessionPath].some((v) => (v ?? "").trim())) return keys;
  // With nothing open, a collapsed Global folder never requests its first page
  // and reads as an empty history, so it starts expanded.
  const global = nodes.find((node) => node?.kind === "global_folder");
  return global ? [projectTreeNodeKey(global, 0)] : [];
}
