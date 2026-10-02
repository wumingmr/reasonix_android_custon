import type { TabMeta, WireEvent } from "./types";

type MockTurnIdentity = { turnId: string; runtimeEpoch: string };

export function withMockSessionIdentity(tab: TabMeta | undefined): TabMeta | undefined {
  if (!tab) return undefined;
  const sessionId = tab.session?.sessionId || tab.sessionId || tab.sessionPath || (tab.topicId ? `mock:${tab.topicId}` : "");
  if (!sessionId) return tab;
  return {
    ...tab,
    sessionId,
    session: tab.session ?? { hostId: "local", sessionId },
    sessionGeneration: tab.sessionGeneration ?? 1,
  };
}

export function mockSessionMeta(tab: TabMeta | undefined) {
  const identified = withMockSessionIdentity(tab);
  return {
    sessionPath: identified?.sessionPath,
    sessionId: identified?.sessionId,
    session: identified?.session,
    sessionGeneration: identified?.sessionGeneration,
  };
}

export function createBrowserMockInteractionIdentity(input: {
  emit: (event: WireEvent) => void;
  currentTabId: () => string | undefined;
  setRunning: (tabId: string | undefined, running: boolean) => void;
}) {
  let sequence = 0;
  const turns = new Map<string, MockTurnIdentity>();
  const current = (): MockTurnIdentity | undefined => {
    const tabId = input.currentTabId();
    return tabId ? turns.get(tabId) : undefined;
  };
  return {
    emitPrompt(event: WireEvent) {
      input.emit({ ...current(), ...event });
    },
    turnStarted(submissionId?: string) {
      const tabId = input.currentTabId();
      const identity = { turnId: `mock-turn-${++sequence}`, runtimeEpoch: `mock-runtime:${tabId ?? "active"}` };
      if (tabId) turns.set(tabId, identity);
      input.setRunning(tabId, true);
      input.emit({ kind: "turn_started", submissionId, ...identity });
    },
    turnDone(submissionId?: string) {
      const tabId = input.currentTabId();
      const identity = current();
      input.setRunning(tabId, false);
      input.emit({ kind: "turn_done", submissionId, ...identity });
      if (tabId) turns.delete(tabId);
    },
  };
}
