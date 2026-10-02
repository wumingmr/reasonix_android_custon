import { runtimeStateStore, type RuntimeSession } from "./runtimeStateStore";
import { attentionChimeEventKey, clearAttentionChimeKeys, playAttentionChime, playSuccessChime, shouldPlayAttentionChimeForEvent, type AttentionChimeEvent } from "./sound";
import type { Translator } from "./i18n";
import type { ToastContextValue } from "./toast";

export type NotificationOperation = { event: AttentionChimeEvent & { err?: string; outcome?: string } } | { resetTabId?: string };

// Outcomes where the turn stopped to wait on the user; the host records them as
// paused or awaiting_delivery (desktop/topic_status.go), never as a finish.
const PAUSED_TURN_OUTCOMES = new Set(["final_readiness", "recovery_paused", "incomplete_read", "completion_uncertain"]);
type NotificationPorts = { activeTabId: string | undefined; t: Translator; showToast: ToastContextValue["showToast"] };

/** One owner deduplicates view events and authoritative background snapshots. */
export function createRuntimeNotifications(readPorts: () => NotificationPorts | undefined) {
  const seen = new Set<string>();
  // Live requests cannot be evicted with historical replay keys. Replace this
  // set on each trusted snapshot so resolved requests do not accumulate.
  let pendingSeen = new Set<string>();
  const handleAttention = (event: AttentionChimeEvent, source?: RuntimeSession) => {
    const ports = readPorts();
    const snapshot = runtimeStateStore.getSnapshot();
    const turnId = event.ask?.turnId || event.approval?.turnId || event.turnId;
    const candidates = snapshot?.sessions.filter(item => (!event.hostId || (item.hostId || "local") === event.hostId)
      && (!turnId || item.state.turnId === turnId)) ?? [];
    const session = source ?? candidates.find(item => item.tabId === event.tabId)
      ?? (turnId && candidates.length === 1 ? candidates[0] : undefined);
    event = { ...event, hostId: session?.hostId || session?.state.hostId || event.hostId };
    const key = attentionChimeEventKey(event);
    if (!ports || !key || pendingSeen.has(key) || !shouldPlayAttentionChimeForEvent(event, seen)) return;
    playAttentionChime();
    const { activeTabId, t, showToast } = ports;
    const background = session ? !session.open || session.tabId !== activeTabId : Boolean(event.tabId && event.tabId !== activeTabId);
    if (!background) return;
    const topic = session ? snapshot?.topics.find(item => item.scope === session.scope
      && (session.scope !== "project" || (item.workspaceRoot ?? "") === session.workspaceRoot)
      && (session.sessionId ? (item.node.session?.sessionId || item.node.remoteSession?.sessionId) === session.sessionId
        && (item.node.session?.hostId || item.node.remoteSession?.hostId || "local") === (session.hostId || "local")
        : session.sessionPath && item.node.sessionPath === session.sessionPath
          && (item.node.remoteSession?.hostId || item.node.session?.hostId || "local") === (session.hostId || "local"))) : undefined;
    const child = session?.sessionPath ? topic?.node.children?.find(node => node.sessionPath === session.sessionPath) : undefined;
    const title = child?.label || topic?.node.label || t("runtime.otherConversation");
    showToast(t(event.kind === "ask_request" ? "runtime.backgroundQuestion" : "runtime.backgroundApproval", { title }), "info", { durationMs: 8000 });
  };
  const handleSnapshot = () => {
    if (runtimeStateStore.getFailed()) return;
    const nextPending = new Set<string>();
    const visit = (event: AttentionChimeEvent, session: RuntimeSession) => {
      event = { ...event, hostId: session.hostId || session.state.hostId };
      const key = attentionChimeEventKey(event);
      if (!key || nextPending.has(key)) return;
      if (session.freshness !== "synced") {
        // Disconnects retain known prompts but cannot announce unseen ones.
        if (pendingSeen.has(key)) nextPending.add(key);
        return;
      }
      handleAttention(event, session);
      nextPending.add(key);
    };
    for (const session of runtimeStateStore.getSnapshot()?.sessions ?? []) {
      if (!session.state.pendingPrompt) continue;
      for (const prompt of session.state.pendingInteractions ?? []) {
        const identity = { id: prompt.requestId, turnId: prompt.turnId || session.state.turnId };
        // Plan/recovery decisions use the same approval card/event path.
        if (prompt.kind === "ask") visit({ kind: "ask_request", tabId: session.tabId, ask: identity }, session);
        else if (["approval", "plan", "recovery"].includes(prompt.kind)) {
          visit({ kind: "approval_request", tabId: session.tabId, approval: identity }, session);
        }
      }
    }
    pendingSeen = nextPending;
  };
  let stop: (() => void) | undefined;
  return {
    accept(operation: NotificationOperation) {
      if (!("event" in operation)) {
        clearAttentionChimeKeys(seen, operation.resetTabId);
        clearAttentionChimeKeys(pendingSeen, operation.resetTabId);
      } else if (operation.event.kind === "turn_done") {
        if (!readPorts()) return;
        if (PAUSED_TURN_OUTCOMES.has(operation.event.outcome ?? "")) playAttentionChime();
        else if (!operation.event.err) playSuccessChime();
      } else handleAttention(operation.event);
    },
    start() { stop = runtimeStateStore.subscribe(handleSnapshot); handleSnapshot(); },
    dispose() { stop?.(); },
  };
}
