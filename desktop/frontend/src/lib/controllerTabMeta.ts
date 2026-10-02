import { modeHasAutoApproveTools, normalizeMode, normalizeToolApprovalMode, type Meta, type TabMeta } from "./types";
import { sameSessionIdentity } from "./sessionIdentity";

export function metaFromTab(tab: TabMeta, existing?: Meta): Meta {
  const cwd = tab.cwd || tab.workspaceRoot || existing?.cwd || "";
  const toolApprovalMode = normalizeToolApprovalMode(
    tab.toolApprovalMode,
    normalizeMode(tab.mode),
    modeHasAutoApproveTools(tab.mode),
    (tab.toolApprovalMode ?? "").trim() === "" ? existing?.toolApprovalMode : undefined,
  );
  const autoApproveTools = toolApprovalMode === "danger-full-access";
  return {
    label: tab.label || existing?.label || "",
    ready: tab.ready,
    runtime: tab.runtime,
    startupErr: tab.startupErr,
    historicalSource: tab.historicalSource,
    eventChannel: existing?.eventChannel ?? "agent:event",
    cwd,
    workspaceRoot: tab.workspaceRoot || existing?.workspaceRoot || cwd,
    workspaceName: tab.workspaceName || existing?.workspaceName,
    workspacePath: tab.workspacePath || tab.workspaceRoot || existing?.workspacePath,
    sessionPath: tab.sessionPath !== undefined ? tab.sessionPath : existing?.sessionPath,
    session: tab.session !== undefined ? tab.session : existing?.session,
    sessionRevision: tab.sessionRevision !== undefined ? tab.sessionRevision : existing?.sessionRevision,
    sessionDigest: tab.sessionDigest !== undefined ? tab.sessionDigest : existing?.sessionDigest,
    sessionGeneration: tab.sessionGeneration !== undefined ? tab.sessionGeneration : existing?.sessionGeneration,
    gitBranch: tab.gitBranch || existing?.gitBranch,
    imageInputEnabled: existing?.imageInputEnabled,
    visionFallbackEnabled: existing?.visionFallbackEnabled,
    autoApproveTools,
    bypass: autoApproveTools,
    collaborationMode: tab.collaborationMode ?? existing?.collaborationMode ?? "normal",
    toolApprovalMode,
    tokenMode: tab.tokenMode ?? existing?.tokenMode ?? "full",
    agentPreset: tab.agentPreset ?? existing?.agentPreset,
    qualityFloor: tab.qualityFloor ?? existing?.qualityFloor,
    floorInferred: tab.floorInferred ?? existing?.floorInferred,
    goal: tab.goal ?? existing?.goal,
    goalStatus: tab.goalStatus ?? existing?.goalStatus,
    goalView: tab.goalView ?? existing?.goalView,
    canonicalTodos: sameSessionIdentity(tab, existing) ? existing?.canonicalTodos : undefined,
  };
}
