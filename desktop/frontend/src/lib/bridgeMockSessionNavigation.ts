import type { SessionRef } from "../generated/desktopContract.generated";
import type { ProjectNode, TabMeta } from "./types";

const children = (node: ProjectNode): ProjectNode[] => Array.isArray(node.children) ? node.children : [];
const sessionIdForNode = (node: ProjectNode) => (node.session?.sessionId || node.topicId || node.key || "mock-session").replace(/[^a-zA-Z0-9._-]/g, "-");
const workspaceIdForNode = (node: ProjectNode) => node.kind === "global_folder" ? "global" : `project-${(node.root || node.key).replace(/[^a-zA-Z0-9._-]/g, "-")}`;

/** Mirror the desktop SessionRef contract: resolve by id and rebind one existing tab in place. */
export function rebindMockSessionTab(
  ref: SessionRef,
  tab: TabMeta | undefined,
  tree: ProjectNode[],
  globalWorkspaceRoot: string,
  isRunning: (topicId: string) => boolean,
): TabMeta {
  if (!tab) throw new Error("mock workspace is not ready");
  const parent = tree.find((candidate) => children(candidate).some((node) => sessionIdForNode(node) === ref.sessionId));
  const node = parent && children(parent).find((candidate) => sessionIdForNode(candidate) === ref.sessionId);
  if (!parent || !node?.topicId) throw new Error(`mock session not found: ${ref.sessionId}`);
  const scope = parent.kind === "global_folder" ? "global" : "project";
  const workspaceRoot = parent.root || (scope === "global" ? globalWorkspaceRoot : "");
  return {
    ...tab, scope, workspaceRoot, workspaceId: workspaceIdForNode(parent), workspaceName: parent.label, workspacePath: workspaceRoot,
    topicId: node.topicId, topicTitle: node.label.replace(/^●\s*/, ""), sessionPath: node.sessionPath || `/mock/sessions/${node.topicId}.jsonl`,
    sessionId: ref.sessionId, session: ref, projectColor: node.projectColor || parent.projectColor,
    ready: true, running: isRunning(node.topicId), active: true, cwd: workspaceRoot,
  };
}
