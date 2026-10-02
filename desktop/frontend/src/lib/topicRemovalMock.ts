import type { ProjectNode } from "./types";
import type { TopicRemovalTarget, TopicRemovalRequest, TopicRemovalInspection, TopicRemovalResult, TrashEntry } from "../generated/desktopContract.generated";
import { mockProjectGroups, notifyMockProjectTreeChanged } from "./mockProjectTreeOrganization";

type Receipt = { request: string; result: TopicRemovalResult; parent: ProjectNode; node: ProjectNode; order: number; entry?: TrashEntry };
const states = new WeakMap<ProjectNode[], { automatic: Set<string>; receipts: Map<string, Receipt>; generation: number }>();
function state(tree: ProjectNode[]) {
  let value = states.get(tree);
  if (!value) { value = { automatic: new Set(), receipts: new Map(), generation: 1 }; states.set(tree, value); }
  return value;
}
export function markTopic(tree: ProjectNode[], id: string, automatic: boolean) {
  if (automatic) state(tree).automatic.add(id); else state(tree).automatic.delete(id);
}
function owner(tree: ProjectNode[], id: string) {
  const parents = tree.filter(parent => parent.children?.some(node => node.topicId === id));
  if (parents.length !== 1) throw new Error("Topic ownership conflict");
  const parent = parents[0], order = parent.children!.findIndex(node => node.topicId === id), node = parent.children![order];
  const workspaceId = parent.kind === "global_folder" ? "global" : `project-${(parent.root || parent.key).replace(/[^a-zA-Z0-9._-]/g, "-")}`;
  return { parent, order, node, workspaceId };
}
export function inspect(tree: ProjectNode[], target: TopicRemovalTarget): TopicRemovalInspection {
  const { parent, node, workspaceId } = owner(tree, target.topicId);
  if (target.workspaceId && target.workspaceId !== workspaceId) throw new Error("Topic ownership conflict");
  const groups = mockProjectGroups(parent.kind === "global_folder" ? "global" : "project", parent.root || "");
  const organized = groups.some(group => group.topicIds?.includes(target.topicId));
  const disposition = node.session ? "archive_sessions" : state(tree).automatic.has(target.topicId) && !node.pinned && !organized ? "discard_placeholder" : "archive_placeholder";
  return { target: { ...target, workspaceId }, disposition, allowed: !node.running, reason: node.running ? "busy" : undefined, token: JSON.stringify([node, groups, disposition]) };
}
export function remove(tree: ProjectNode[], request: TopicRemovalRequest): TopicRemovalResult {
  const saved = state(tree), encoded = JSON.stringify(request), previous = saved.receipts.get(request.operationId);
  const conflict = { committed: false, disposition: "", retryable: false, errorCode: "state_conflict" };
  if (previous) return previous.request === encoded ? previous.result : conflict;
  const view = inspect(tree, request.target);
  if (!view.allowed || view.token !== request.expectedToken) return conflict;
  const { parent, node, order, workspaceId } = owner(tree, request.target.topicId);
  const result: TopicRemovalResult = { committed: true, disposition: view.disposition, retryable: false };
  const receipt: Receipt = { request: encoded, result, parent, node: structuredClone(node), order };
  if (view.disposition !== "discard_placeholder") {
    result.recoveryEntryId = `topic-removal:${request.operationId}`;
    receipt.entry = { id: result.recoveryEntryId, recoveryEntryId: result.recoveryEntryId, workspaceId, workspaceTitle: parent.label, title: node.label,
      archivedAt: Date.now(), health: "ready", canPreview: false, canRestore: true, canPurge: true };
  }
  parent.children!.splice(order, 1);
  saved.receipts.set(request.operationId, receipt); saved.generation++;
  notifyMockProjectTreeChanged();
  return result;
}
export function trash(tree: ProjectNode[]) {
  const saved = state(tree);
  return { generation: saved.generation, items: [...saved.receipts.values()].flatMap(receipt => receipt.entry ? [receipt.entry] : []) };
}
export function recover(tree: ProjectNode[], id: string, workspaceId: string, action: string) {
  const saved = state(tree), receipt = saved.receipts.get(id.replace(/^topic-removal:/, ""));
  if (!receipt?.entry || receipt.entry.workspaceId !== workspaceId) throw new Error("Recovery conflict");
  if (action === "restore") {
    if (!tree.includes(receipt.parent) || tree.some(parent => parent.children?.some(node => node.topicId === receipt.node.topicId))) throw new Error("Restore conflict");
    (receipt.parent.children ??= []).splice(receipt.order, 0, structuredClone(receipt.node));
  } else if (action !== "purge") throw new Error("Invalid metadata lifecycle action");
  receipt.entry = undefined; saved.generation++;
  notifyMockProjectTreeChanged();
}
