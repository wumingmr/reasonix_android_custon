// Actual product components and command owners; only the Desktop RPC boundary
// is replaced. No production mock data or identity implementation is changed.
import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { ProjectTree } from "../src/components/ProjectTree";
import { HistoryPanel } from "../src/components/HistoryPanel";
import { TopicbarRegion } from "../src/app-shell/TopicbarRegion";
import { useProjectTopicCommands } from "../src/app-runtime/useProjectTopicCommands";
import { useHistoryCommands } from "../src/app-runtime/useHistoryCommands";
import { desktopProjectAdapter } from "../src/app-runtime/desktopProjectAdapter";
import type { HistoryViewState } from "../src/app-runtime/historyViewProjection";
import { app } from "../src/lib/bridge";
import { DESKTOP_COMMANDS, type SessionSelector, type SessionOrganizationMutation, type SessionOrganizationSnapshot } from "../src/generated/desktopContract.generated";
import { ApprovalModal } from "../src/components/ApprovalModal";
import { useSessionOperations } from "../src/app-runtime/useSessionOperations";
import { useSessionPromptCommands } from "../src/app-runtime/useSessionPromptCommands";
import { useSessionControlCommands } from "../src/app-runtime/useSessionControlCommands";
import { resolvePromptForTab } from "../src/lib/exactPromptSubmit";
import { requestSessionCancel } from "../src/lib/inboxCancel";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import { LocaleProvider } from "../src/lib/i18n";
import { ToastProvider } from "../src/lib/toast";
import type { ProjectNode, SessionMeta } from "../src/lib/types";
import "../src/styles.css";
import { rememberProjectTreeWindowLimit, projectTreeListKey } from "../src/lib/projectTreeWindow";

localStorage.setItem("reasonix-lang", "en");
const root = "/fixture/independent";
const topicId = "same-topic";
const paginationScenario = new URLSearchParams(location.search).has("pagination");
const largeWindow = Number(new URLSearchParams(location.search).get("window") || 0);
let paginationInvalidated = false;
const pageRequests: { cursor: string; revision: number; rejected?: boolean }[] = [];
let revision = 1;
let stale = false;
let archived = false;
const rows: ProjectNode[] = (largeWindow ? Array.from({ length: largeWindow }, (_, i) => String(i)) : paginationScenario ? ["a", "b", "c", "d", "e", "f", "g"] : ["a", "b"]).map((id, index) => ({
  key: `session-${id}`, kind: "topic", label: `Session ${id.toUpperCase()}`, root, topicId,
  session: { hostId: "local", sessionId: id }, sessionPath: `session-id:${id}`,
  lifecycleGeneration: 1, resultSequence: index === 0 ? 100 : 10,
  turns: 1, turnsState: "ready", createdAt: 100, lastActivityAt: 100, children: [],
}));
const folder: ProjectNode = { key: "project-fixture", kind: "project", label: "Independent sessions", root, children: [] };
if (largeWindow) rememberProjectTreeWindowLimit(projectTreeListKey(folder.key), largeWindow);
const catalogStatus = () => ({ state: "ready", mode: "memory", revision, indexed: 2, total: 2, repairPending: 0, sourceCount: 2, unindexedTargetCount: 0, canRebuild: false });
const listRows = () => structuredClone(rows.filter(row => !archived || stale || row.session?.sessionId !== "b"));
const sessions = (): SessionMeta[] => rows.filter(row => !archived || row.session?.sessionId !== "b").map(row => ({
  path: row.sessionPath!, sessionId: row.session!.sessionId, hostId: "local", topicId,
  title: row.label, topicTitle: row.label, preview: row.label, turns: 1, createdAt: 100, lastActivityAt: 100, modTime: 100,
  current: false, open: false, scope: "project", workspaceRoot: root,
}));
const calls: { method: string; id: string; title?: string }[] = [];
const organizationCalls: SessionOrganizationMutation[] = [];
const runtimeCalls: { method: string; tabId: string; promptId?: string; turnId?: string; epoch?: string }[] = [];
const organization: SessionOrganizationSnapshot = { revision: 1, applied: true, groups: [], order: ["ref\x00local\x00a", "ref\x00local\x00b"], manualOrderEnabled: false };
const resolve = (selector: SessionSelector) => {
  const row = rows.find(row => selector.ref ? row.session?.sessionId === selector.ref.sessionId && selector.ref.hostId === "local" : row.sessionPath === selector.sessionPath);
  if (!row) throw new Error("fixture target_not_found");
  return row;
};
const fallback = Object.fromEntries(DESKTOP_COMMANDS.map(name => [name, app[name]]));
const host = installDesktopHostStub({ ...fallback,
  Platform: async () => "linux",
  GetProjectTreeSnapshot: async () => ({ revision, projects: [folder], catalog: catalogStatus(), indexed: 2, total: 2, indexingDone: true }),
  ListProjectTopics: async (request: { groupId?: string; cursor?: string; limit?: number }) => {
    if (largeWindow) {
      const entry = { cursor: request.cursor || "", revision, rejected: false }; pageRequests.push(entry);
      if (request.cursor === "old:200") { paginationInvalidated = true; entry.rejected = true; throw new Error("session_operation:stale_cursor:expired"); }
      const start = Number(request.cursor?.split(":")[1] || 0), end = Math.min(rows.length, start + (request.limit || 5));
      const id = paginationInvalidated ? "new" : "old";
      return { revision, snapshotId: id, items: structuredClone(rows.slice(start, end)).map((row, i) => ({ ...row, sortOrder: start+i })), nextCursor: end < rows.length ? `${id}:${end}` : "", complete: true, readyDirectories: 1, pendingDirectories: 0, failedDirectories: 0 };
    }
    if (paginationScenario) {
      const entry = { cursor: request.cursor || "", revision, rejected: false };
      pageRequests.push(entry);
      if (request.cursor === "old:5") {
        paginationInvalidated = true; revision++; entry.rejected = true;
        throw new Error("session_operation:stale_cursor:The session list changed. Reload it.");
      }
      const ids = paginationInvalidated ? ["c", "a", "b", "d", "e", "f", "g"] : ["a", "b", "d", "e", "f", "g"];
      const start = request.cursor ? Number(request.cursor.split(":")[1]) : 0;
      const stop = Math.min(ids.length, start + (request.limit ?? 5));
      const items = ids.slice(start, stop).map((id, index) => ({ ...structuredClone(rows.find(row => row.session!.sessionId === id)!), sortOrder: start + index }));
      return { revision, items, nextCursor: stop < ids.length ? `${paginationInvalidated ? "new" : "old"}:${stop}` : "", complete: true, readyDirectories: 1, pendingDirectories: 0, failedDirectories: 0 };
    }
    const members = organization.groups.find(group => group.id === request.groupId)?.sessionKeys ?? [];
    const grouped = new Set(organization.groups.flatMap(group => group.sessionKeys ?? []));
    const items = listRows().filter(row => request.groupId ? members.includes(`ref\x00local\x00${row.session!.sessionId}`) : !grouped.has(`ref\x00local\x00${row.session!.sessionId}`));
    if (organization.manualOrderEnabled) {
      for (const row of items) row.sortOrder = organization.order.indexOf(`ref\x00local\x00${row.session!.sessionId}`);
      items.sort((a,b) => a.sortOrder! - b.sortOrder!);
    }
    return { revision, items, nextCursor: "", complete: true, readyDirectories: 1, pendingDirectories: 0, failedDirectories: 0 };
  },
  GetSessionCatalogStatus: async () => catalogStatus(),
  GetProjectTreeRuntimeSnapshot: async () => ({ revision, topics: [] }),
  GetTopicSummary: async () => ({ key: "", kind: "topic", label: "", children: [] }),
  GetSessionOrganization: async () => structuredClone(organization),
  UpdateSessionOrganization: async (_workspace: unknown, expectedRevision: number, mutation: SessionOrganizationMutation) => {
    if (expectedRevision !== organization.revision) return { ...structuredClone(organization), applied: false };
    organizationCalls.push(structuredClone(mutation));
    const key = mutation.target ? `ref\x00local\x00${resolve(mutation.target).session!.sessionId}` : "";
    if (mutation.kind === "create-group") organization.groups.push({ id: mutation.groupId!, title: mutation.title!, sessionKeys: [] });
    else if (mutation.kind === "set-group") {
      for (const group of organization.groups) group.sessionKeys = (group.sessionKeys ?? []).filter(item => item !== key);
      organization.groups.find(group => group.id === mutation.groupId)?.sessionKeys?.push(key);
    } else if (mutation.kind === "move") {
      const anchor = `ref\x00local\x00${resolve(mutation.anchor!).session!.sessionId}`;
      organization.order = organization.order.filter(item => item !== key);
      organization.order.splice(organization.order.indexOf(anchor) + (mutation.position === "after" ? 1 : 0), 0, key);
      organization.manualOrderEnabled = true;
    } else throw new Error(`unhandled organization mutation ${mutation.kind}`);
    organization.revision++; revision++;
    return structuredClone(organization);
  },
  ResolvePromptForTab: async (tabId: string, promptId: string, turnId: string, epoch: string) => { runtimeCalls.push({ method: "approve", tabId, promptId, turnId, epoch }); },
  CancelSessionForTab: async (tabId: string) => { runtimeCalls.push({ method: "stop", tabId }); return { accepted: true, alreadyIdle: false, recoveryRequired: false }; },
  GetProjectGroups: async () => ({ revision: 1, groups: [], applied: true }),
  ListProjectGroups: async () => [],
  ListTabs: async () => [],
  RemoteConnectionStatuses: async () => [],
  ListSessions: async () => sessions(),
  ListHistorySessions: async () => ({ items: sessions(), nextCursor: "", revision, partial: false }),
  GetSessionRecoveryVersions: async () => ({ items: [] }),
  GetSessionActivityBaseline: async (selector: SessionSelector) => ({ ref: resolve(selector).session, complete: true, resultSequence: resolve(selector).resultSequence, eventVersion: "100", lifecycleGeneration: 1 }),
  RenameTopic: async () => { throw new Error("single-session UI called forbidden topic bulk rename"); },
  RenameSessionTarget: async (selector: SessionSelector, title: string) => {
    const row = resolve(selector); row.label = title; revision++;
    calls.push({ method: "rename", id: row.session!.sessionId, title });
    host.emit("project-tree:changed-v2", { revision, roots: [root], reason: "metadata" });
    host.emit("history-index:changed-v1", { revision, indexed: 2, total: 2, pending: 0 });
    return { committed: true, operationId: `rename-${revision}`, lifecycleGeneration: 1, targetKey: `ref\x00local\x00${row.session!.sessionId}` };
  },
  ArchiveSessionTarget: async (selector: SessionSelector) => {
    const row = resolve(selector);
    if (row.session!.sessionId !== "b") throw new Error("archive targeted sibling A");
    archived = true; revision++;
    calls.push({ method: "archive", id: "b" });
    return { committed: true, operationId: "archive-b", lifecycleGeneration: 2, targetKey: "ref\x00local\x00b" };
  },
});
(window as any).__independentEvidence = { calls, rows, organizationCalls, organization, runtimeCalls, pageRequests };

function Fixture() {
  const [selected, setSelected] = useState(largeWindow ? "0" : "a");
  const [refresh, setRefresh] = useState(0);
  const [mount, setMount] = useState(0);
  const [variant, setVariant] = useState<"workbench" | "creation">("workbench");
  const [history, setHistory] = useState<HistoryViewState | null>(null);
  const [showApproval, setShowApproval] = useState(false);
  const [activityStep, setActivityStep] = useState(0);
  const target = { tabId: `tab-${selected}`, sessionKey: selected };
  const resources = (largeWindow ? ["0"] : ["a", "b"]).map(id => ({ tabId: `tab-${id}`, sessionKey: id }));
  const operations = useSessionOperations({ visible: target, resources });
  const promptCommands = useSessionPromptCommands({ target, session: { hostId: "local", sessionId: selected }, sessionGeneration: 1, approval: { id: `approval-${selected}`, tool: "bash" }, remote: false, goal: "", toolApprovalMode: "read-only", operations,
    reportError: error => { throw error; }, ports: {
      isPromptCurrentForTab: target => target.promptId === `approval-${target.tabId.replace("tab-", "")}`,
      approveForTab: async (target, allow, session, persist) => { await resolvePromptForTab(app, target.tabId, target.promptId, "approval", { allow, session, persist }, `turn-${target.tabId}`, `epoch-${target.tabId}`); setShowApproval(false); },
      resolvePlanForTab: () => {}, resolveRecoveryForTab: () => {}, answerQuestionForTab: async () => {}, answerMCPForTab: () => {},
      setCollaborationModeForTab: async () => {}, clearGoalForTab: async () => {}, setRemoteComposerProfile: async () => [],
      patchComposerProfile: () => {}, notePlanMode: () => {}, drainRemoteApprovals: () => {}, rememberRevision: () => {},
    } });
  const controlCommands = useSessionControlCommands({ activeTabId: target.tabId, resources, operations, showToast: message => { throw new Error(message); }, clearWorkspaceConflict: () => {}, ports: {
    cancel: async () => { throw new Error("stop must retain its source tab"); }, cancelForTab: (tab, items) => requestSessionCancel(app, tab, items),
    acceptDelivery: async () => {}, disconnectRemote: async () => {}, cancelJobForTab: async () => false, refreshBackgroundRuntimes: async () => {},
  } });
  const row = rows.find(row => row.session?.sessionId === selected)!;
  const commands = useProjectTopicCommands({
    visible: { tabId: `tab-${selected}`, sessionKey: selected },
    topic: { id: topicId, title: row.label, target: { kind: "local", topicId, selector: { ref: row.session } } },
    ports: { ...desktopProjectAdapter, markChanged: () => setRefresh(value => value + 1), refreshTabs: async () => [{ id: `tab-${selected}` }], syncActive: async () => {} },
    navigation: { openBlank: async () => {}, enqueue: async () => {}, switchFolder: async () => {} }, reportError: error => { throw error; },
  });
  const historyCommands = useHistoryCommands({ running: false, setHistView: setHistory,
    ports: { listSessions: async () => sessions(), deleteSession: async () => {}, renameSession: async () => { throw new Error("ambiguous rename"); }, openPage: () => {} } });
  return <div className={`app app--${variant}`} style={{ display: "block", height: "100vh" }}>
    <div style={{ padding: 12, display: "flex", gap: 12 }}>
      <button onClick={() => setHistory({ kind: "history", source: "all", sessions: sessions() })}>Open fixture history</button>
      <button onClick={() => { document.documentElement.dataset.theme = "light"; }}>Light theme</button>
      <button onClick={() => { document.documentElement.dataset.theme = "dark"; }}>Dark theme</button>
      <button onClick={() => setVariant(value => value === "workbench" ? "creation" : "workbench")}>Toggle creation</button>
      <button onClick={() => setMount(value => value + 1)}>Remount sidebar</button>
      <button onClick={() => setShowApproval(true)}>Show selected runtime approval</button>
      <button onClick={() => {
        rows[1]!.resultSequence = activityStep === 1 ? 10 : 20;
        setActivityStep(value => value + 1); revision++; setRefresh(value => value + 1);
        host.emit("project-tree:changed-v2", { revision, roots: [root], reason: "metadata" });
        setMount(value => value + 1);
      }}>Advance B activity observation</button>
      <button onClick={() => {
        stale = true; revision++;
        host.emit("project-tree:runtime-changed", { revision, topics: [{ scope: "project", workspaceRoot: root, node: { ...rows[1], runtimeOnly: true, running: true, status: "thinking" } }] });
        setRefresh(value => value + 1);
      }}>Inject late snapshots</button>
    </div>
    <TopicbarRegion view={{ automationReturn: false, automationReturnLabel: "", chromeHidden: false, brand: false,
      sidebar: { title: "Sidebar", blocked: false, pressed: false, collapsed: false },
      title: { text: row.label, hover: row.label, renameLabel: "Rename selected session", editing: commands.topicbarEditing, draft: commands.topicTitleDraft, canRename: true },
      subtitle: { visible: false, title: "", mergeLabel: "", mergeTooltip: "" },
    }} commands={{ openAutomation: () => {}, toggleSidebar: () => {}, setTitleDraft: commands.setTopicTitleDraft,
      commitRename: commands.commitActiveTopicRename, cancelRename: commands.cancelActiveTopicRename,
      startRename: commands.startActiveTopicRename, openWorktree: () => {} }}>{null}</TopicbarRegion>
    <aside className={`sidebar sidebar--${variant}`} style={{ width: 360, height: "calc(100vh - 120px)", position: "relative" }}>
      <ProjectTree key={mount} variant={variant} activeScope="project" activeWorkspaceRoot={root} activeTopicId={topicId} activeSessionPath={`session-id:${selected}`}
        refreshSignal={refresh} onOpenTopic={async (_scope, _root, _topic, path) => { setSelected(path?.replace("session-id:", "") ?? "a"); }}
        onAddProject={async () => {}} onTopicsChanged={() => setRefresh(value => value + 1)} />
    </aside>
    {showApproval && <div style={{ position: "fixed", bottom: 20, left: 400, right: 20 }}><ApprovalModal key={selected}
      approval={{ id: `approval-${selected}`, tool: "bash", subject: `echo Session ${selected.toUpperCase()}`, fresh: true }}
      tabId={target.tabId} onAnswer={promptCommands.handleApprovalAnswer} onStop={() => { void controlCommands.handleCancelActive(); setShowApproval(false); }} /></div>}
    {history && <HistoryPanel sessions={history.sessions} running={false} onResume={s => { setSelected(s.sessionId!); setHistory(null); }}
      onPreview={async () => []} onDelete={() => {}} onRename={historyCommands.onRenameHistorySession} onClose={() => setHistory(null)} />}
  </div>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><ToastProvider><Fixture /></ToastProvider></LocaleProvider>);
