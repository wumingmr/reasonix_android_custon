import { app } from "../lib/bridge";

/** Runtime-only bridge ports used by App owners; presentation never imports the desktop bridge directly. */
export const desktopBridge = {
  setRemoteTabComposerProfile: (tabId: string, mode: string, approvalMode: string, goal: string) =>
    app.SetRemoteTabComposerProfile(tabId, mode, approvalMode, goal),
  getTopicSummary: (request: Parameters<typeof app.GetTopicSummary>[0]) => app.GetTopicSummary(request),
  cancelJobForTab: (tabId: string, jobId: string) => app.CancelJobForTab(tabId, jobId),
  clearRemoteTabSession: (tabId: string) => app.ClearRemoteTabSession(tabId),
  terminalOutputForTab: (tabId: string, sessionId: string) => app.TerminalOutputForTab(tabId, sessionId),
  acceptDeliveryToTab: (tabId: string) => app.AcceptDeliveryToTab(tabId),
  disconnectRemoteHost: (hostId: string) => app.DisconnectRemoteHost(hostId),
  openRemoteProjectTab: app.OpenRemoteProjectTab,
  listTabs: app.ListTabs,
  openTaskSessionForTab: app.OpenTaskSessionForTab,
  listSessionsForTab: app.ListSessionsForTab,
  prepareSession: app.PrepareSession!,
  getSessionPreparation: app.GetSessionPreparation!,
  closeMergedWorktreeTab: app.CloseMergedWorktreeTab,
  finalizeWorktreeMerge: app.FinalizeWorktreeMerge,
};
