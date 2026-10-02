import { CommandCancelled } from "../lib/commandOutcome";
import { sessionTitleErrorKey } from "../lib/sessionTitleOperation";
import type { SessionRef } from "../lib/sessionRef";
import type { RemoteTabOpenOptions, RemoteTabRefView, SessionMeta, TabMeta } from "../lib/types";
import type { useAppRuntimeAdapter } from "./useAppRuntimeAdapter";
import { isChannelSession, sidebarImSessionTarget, type SidebarImConnection } from "./sidebarImProjection";
import type { SessionOperationAuthority } from "./useResourceOperations";
import type { SessionPreparationView, SessionSelector } from "../generated/desktopContract.generated";

export type DesktopNavigationIntent =
  | { kind: "canonical-session"; ref: SessionRef }
  | { kind: "topic"; scope: string; workspaceRoot: string; topicId: string; sessionPath?: string }
  | { kind: "blank"; scope: string; workspaceRoot: string }
  | { kind: "isolated-worktree"; workspaceRoot: string }
  | { kind: "sidebar-im"; connection: SidebarImConnection }
  | { kind: "resume-session"; session: SessionMeta }
  | { kind: "remote-project"; remote: RemoteTabRefView; options: RemoteTabOpenOptions };
type Runtime = ReturnType<typeof useAppRuntimeAdapter>;
export type DesktopNavigationPorts = Pick<Runtime["navigation"],
  "isNavigationIntentCurrent" | "activateTopic" | "openCanonicalSession"
  | "ensureBlankSurface" | "createIsolatedWorktree" | "registeredNavigationIntent" | "switchRemoteTab"> &
  Pick<Runtime["sessionActions"], "openChannelSession" | "resumeSession"> & {
    listTabs(): Promise<TabMeta[]>;
    openRemoteProject(hostId: string, workspace: string, options: RemoteTabOpenOptions): Promise<TabMeta>;
    applyTabs(tabs: TabMeta[]): void;
    seedTab(tab: TabMeta): void;
    topicAccepted?(intent: number): void;
    reveal(): void;
    projectChanged(): void;
    closeHistory(): void;
    listSessions(): Promise<SessionMeta[]>;
    applyHistorySessions(sessions: SessionMeta[]): void;
    prepareSession(selector: SessionSelector): Promise<SessionPreparationView>;
    getSessionPreparation(operationId: string): Promise<SessionPreparationView>;
    notice(notice: NavigationNotice): void;
  };
export type HistoricalPreparationSurface = {
  session: SessionMeta;
  operationId: string;
  status: SessionPreparationView["status"];
  errorCode?: string;
  errorDetail?: string;
  retryable: boolean;
  revision?: number;
  isCurrent?: () => boolean;
};
let historicalPreparation: HistoricalPreparationSurface | null = null;
const historicalPreparationListeners = new Set<() => void>();
export const historicalPreparationSnapshot = () => historicalPreparation;
export const subscribeHistoricalPreparation = (listener: () => void) => {
  historicalPreparationListeners.add(listener);
  return () => historicalPreparationListeners.delete(listener);
};
export const setHistoricalPreparation = (surface: HistoricalPreparationSurface | null) => {
  historicalPreparation = surface;
  historicalPreparationListeners.forEach(listener => listener());
};
export function reconcileHistoricalPreparation(expected: HistoricalPreparationSurface, view: SessionPreparationView) {
  const current = historicalPreparation;
  if (!current || current.operationId !== expected.operationId || current.session !== expected.session
    || expected.isCurrent?.() === false || view.operationId !== expected.operationId
    || view.revision < (current.revision ?? 0) || view.status === "ready") return;
  setHistoricalPreparation({ ...current, status: view.status, errorCode: view.errorCode, errorDetail: view.errorDetail,
    retryable: view.retryable, revision: view.revision });
}
export type NavigationNotice = {
  key: "history.failedOpenSession" | "history.missingWorkspaceRoot" | "history.failedOpenProject" | "sidebar.imWaiting" | "sidebar.imOpenFailed"
    | "projectTree.worktreeCreated" | "projectTree.worktreeCreatedDirty" | ReturnType<typeof sessionTitleErrorKey>;
  params?: Record<string, string>;
  tone?: "error" | "warn" | "info";
  durationMs?: number;
} | { message: string; tone?: "error"; durationMs?: number };
export type DesktopNavigationCapture = {
  intent: DesktopNavigationIntent;
  navigationIntentSeq: number;
  ports: DesktopNavigationPorts;
};
class InvalidSessionTarget extends Error {
  constructor(readonly key: "history.failedOpenSession" | "history.missingWorkspaceRoot") { super(key); }
}

/** One executor for topic, blank, IM, worktree and history activation. */
export async function executeDesktopNavigation(input: DesktopNavigationCapture, authority: SessionOperationAuthority) {
  const { navigationIntentSeq: seq, ports } = input;
  let request = input.intent;
  const checkpoint = () => {
    authority.checkpoint();
    if (!ports.isNavigationIntentCurrent(seq)) throw new CommandCancelled("superseded");
  };
  const refresh = async () => {
    const tabs = await ports.listTabs().catch(() => []);
    checkpoint();
    ports.applyTabs(tabs);
  };
  const openTopic = (scope: string, workspace: string, topic: string, path?: string) =>
    ports.activateTopic(scope, workspace, topic, path || "", seq);
  const openBlank = (scope: string, workspace: string) =>
    ports.ensureBlankSurface(scope, scope === "project" ? workspace : "", seq);
  checkpoint();
  setHistoricalPreparation(null);
  try {
    if (request.kind === "topic" && request.sessionPath?.startsWith("session-source:")) {
      const { title, ...source } = JSON.parse(decodeURIComponent(request.sessionPath.slice("session-source:".length)));
      request = { kind: "resume-session", session: { scope: request.scope, workspaceRoot: request.workspaceRoot,
        topicId: request.topicId, title, path: source.path, source } as SessionMeta };
    }
    if (request.kind === "canonical-session") {
      await ports.openCanonicalSession(request.ref, seq);
      checkpoint(); ports.closeHistory();
      await refresh(); checkpoint(); ports.reveal(); return;
    }
    if (request.kind === "remote-project") {
      const token = await ports.registeredNavigationIntent(seq);
      checkpoint();
      if (!token) throw new CommandCancelled("superseded");
      const tab = await ports.openRemoteProject(request.remote.hostId, request.remote.workspace, request.options);
      checkpoint(); ports.seedTab(tab);
      await ports.switchRemoteTab(tab, seq);
      checkpoint(); ports.reveal();
      await refresh();
      return tab;
    }
    if (request.kind === "topic" || request.kind === "blank") {
      const tab = request.kind === "topic"
        ? await openTopic(request.scope, request.workspaceRoot, request.topicId, request.sessionPath)
        : await openBlank(request.scope, request.workspaceRoot);
      checkpoint(); ports.seedTab(tab);
      if (request.kind === "topic") ports.topicAccepted?.(seq);
      if (request.kind === "blank") ports.projectChanged();
      if (request.kind === "topic") { ports.reveal(); await refresh(); }
      else { await refresh(); checkpoint(); ports.reveal(); }
      return;
    }
    if (request.kind === "isolated-worktree") {
      const result = await ports.createIsolatedWorktree(request.workspaceRoot, seq);
      checkpoint(); ports.seedTab(result.tab); ports.projectChanged();
      await refresh(); checkpoint();
      ports.notice({ key: result.sourceDirty ? "projectTree.worktreeCreatedDirty" : "projectTree.worktreeCreated",
        params: { branch: result.branch }, tone: result.sourceDirty ? "warn" : "info", durationMs: result.sourceDirty ? 7000 : 3500 });
      ports.reveal(); return;
    }
    if (request.kind === "sidebar-im") {
      const { connection } = request;
      const target = sidebarImSessionTarget(connection);
      if (!target) { ports.notice({ key: "sidebar.imWaiting", params: { name: connection.title } }); return; }
      let tab: TabMeta;
      if (target.kind === "path") {
        tab = await openBlank(connection.scope, connection.workspaceRoot);
        checkpoint();
        if (connection.sessionSource === "auto") await ports.openChannelSession(target.value, tab.id, seq);
        else await ports.resumeSession(target.value, tab.id, seq);
      } else if (target.kind === "bot-session") {
        tab = await openTopic(connection.scope, connection.workspaceRoot, "", `bot-session:local:${target.value}`);
      } else tab = await openTopic(connection.scope, connection.workspaceRoot, target.value);
      checkpoint(); ports.seedTab(tab);
      await refresh(); checkpoint(); ports.reveal(); ports.projectChanged(); return;
    }
    const { session } = request;
    const scope = session.scope || (session.workspaceRoot ? "project" : "global");
    let tab: TabMeta;
    if (isChannelSession(session)) {
      tab = await openBlank(scope === "project" ? "project" : "global", session.workspaceRoot || "");
      checkpoint(); await ports.openChannelSession(session.path, tab.id, seq);
    } else if (session.sessionId && (!session.hostId || session.hostId === "local")) {
      tab = await openTopic(scope, session.workspaceRoot || "", session.topicId || `canonical-${session.sessionId}`, `session-id:${session.sessionId}`);
	} else if (session.source) {
		// Historical sources are opened in place. Explicit conversion remains a
		// separate user action and never blocks normal navigation.
		tab = await openTopic(scope, session.workspaceRoot || "", session.topicId || "", `session-source:${encodeURIComponent(JSON.stringify(session.source))}`);
    } else if (scope === "project" && session.workspaceRoot && session.topicId) {
      tab = await openTopic("project", session.workspaceRoot, session.topicId, session.path);
    } else if (scope === "global" && session.topicId) {
      tab = await openTopic("global", "", session.topicId, session.path);
    } else throw new InvalidSessionTarget(scope === "global" && !session.topicId
      ? "history.failedOpenSession" : session.topicId ? "history.missingWorkspaceRoot" : "history.failedOpenSession");
    checkpoint(); ports.seedTab(tab); ports.closeHistory();
    if (input.intent.kind === "topic") ports.topicAccepted?.(seq);
    ports.reveal(); await refresh();
  } catch (error) {
    checkpoint();
    if (request.kind === "canonical-session") {
      ports.notice({ message: error instanceof Error ? error.message : String(error), tone: "error" });
      return;
    }
    if (request.kind === "remote-project") throw error;
    if (request.kind === "topic" || request.kind === "blank") {
      ports.notice({ key: "history.failedOpenSession", tone: "error" });
      await refresh(); return;
    }
    if (request.kind === "isolated-worktree") {
      ports.notice({ message: error instanceof Error ? error.message : String(error), tone: "error", durationMs: 6000 }); return;
    }
    if (request.kind === "sidebar-im") { ports.notice({ key: "sidebar.imOpenFailed", params: { name: request.connection.title } }); return; }
    const history = await ports.listSessions().catch(() => null);
    checkpoint();
    if (history) ports.applyHistorySessions(history);
    const message = error instanceof Error ? error.message : String(error ?? "");
    if (/no such file|cannot find the file|file does not exist|session is pending cleanup|session .*not found/i.test(message)) return;
    ports.closeHistory();
    ports.notice(error instanceof InvalidSessionTarget ? { key: error.key }
      : { key: sessionTitleErrorKey(error), tone: "error" });
  }
}
