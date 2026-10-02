import { CommandCancelled } from "../lib/commandOutcome";
import type { RemoteSessionView } from "../lib/remoteTypes";
import type { SessionOperationAuthority } from "./useResourceOperations";
import type { SessionSelector } from "../generated/desktopContract.generated";

export type TopicRenameTarget =
  | { kind: "local"; topicId: string; selector: SessionSelector }
  | { kind: "remote"; hostId: string; workspace: string; sessionPath: string; sessionId?: string };
export type ProjectTopicPorts = {
  renameLocal: (selector: SessionSelector, title: string) => Promise<unknown>;
  listRemote: (host: string, workspace: string) => Promise<RemoteSessionView[]>;
  renameRemote: (host: string, workspace: string, name: string, title: string) => Promise<void>;
  markChanged: (update: (value: number) => number) => void;
  refreshTabs: (apply?: () => boolean, options?: { afterMutation?: boolean }) => Promise<readonly { id: string }[]>;
  syncActive: (rebuild: boolean) => Promise<unknown>;
};
export type ProjectRefreshInput = { activeTabId?: string; ports: ProjectTopicPorts };

export async function refreshProjectTopics(input: ProjectRefreshInput, authority: SessionOperationAuthority) {
  authority.checkpoint();
  input.ports.markChanged(value => value + 1);
  const tabs = await input.ports.refreshTabs(() => {
    try { authority.checkpoint(); return true; } catch { return false; }
  }, { afterMutation: true });
  authority.checkpoint();
  if (authority.ownsUI() && input.activeTabId && !tabs.some(tab => tab.id === input.activeTabId)) await input.ports.syncActive(false);
}

export async function renameProjectTopic(input: ProjectRefreshInput & { target: TopicRenameTarget; title: string }, authority: SessionOperationAuthority) {
  const { target, title, ports } = input;
  authority.checkpoint();
  if (target.kind === "local") await ports.renameLocal(target.selector, title);
  else {
    const sessions = await ports.listRemote(target.hostId, target.workspace);
    authority.checkpoint();
    // `current` is a navigation snapshot, not the identity of the rename target.
    const matches = sessions.filter(session => target.sessionId
      ? session.sessionId === target.sessionId
      : Boolean(target.sessionPath) && session.path === target.sessionPath);
    if (matches.length === 0) throw new CommandCancelled("superseded");
    const source = matches[0];
    if (matches.length !== 1 || !source.name || sessions.filter(session => session.name === source.name).length !== 1) {
      throw new Error("The remote service cannot uniquely address this session. Upgrade the remote service before renaming it.");
    }
    await ports.renameRemote(target.hostId, target.workspace, source.name, title);
  }
  authority.checkpoint();
  await refreshProjectTopics(input, authority);
}
