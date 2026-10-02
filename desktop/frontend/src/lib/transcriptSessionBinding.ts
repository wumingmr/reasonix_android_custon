import type { SessionTranscript } from "./transcriptStoreTypes";
import { releaseHistoryRead } from "./historyReadScope";

export type TranscriptTabBinding = {
  key: string;
  sessionPath: string;
  bindingKey: string;
};

function legacySessionKeyFor(tabId: string, sessionPath: string): string {
  return `${tabId}\n${sessionPath}`;
}

function stableSessionKeyFor(bindingKey: string): string | undefined {
  return bindingKey.startsWith("s\0") ? `stable\0${bindingKey}` : undefined;
}

export function boundSessionKey(
  bindings: Map<string, TranscriptTabBinding>,
  tabId: string,
  sessionPath: string,
): string {
  const binding = bindings.get(tabId);
  return binding?.sessionPath === sessionPath ? binding.key : legacySessionKeyFor(tabId, sessionPath);
}

/** Eviction may retire an old canonical cache after its tab has been rebound. */
export function releaseTranscriptSessionRead(bindings: Map<string, TranscriptTabBinding>, session: SessionTranscript): void {
  if (bindings.get(session.tabId)?.key === session.key) releaseHistoryRead(session.tabId);
}

export function bindTranscriptSession(
  bindings: Map<string, TranscriptTabBinding>,
  sessions: Map<string, SessionTranscript>,
  tabId: string,
  sessionPath: string,
  bindingKey: string,
  create: (key: string) => SessionTranscript,
  evict: (session: SessionTranscript) => void,
  touch: (session: SessionTranscript) => void,
): boolean {
  const previous = bindings.get(tabId);
  const key = stableSessionKeyFor(bindingKey) ?? legacySessionKeyFor(tabId, sessionPath);
  if (previous?.key === key && previous.sessionPath === sessionPath && previous.bindingKey === bindingKey) return false;
	if (previous) releaseHistoryRead(tabId);
  const replaced = Boolean(previous);
  bindings.delete(tabId);
  if (previous && (previous.key !== key || (!key.startsWith("stable\0") && previous.bindingKey !== bindingKey))) {
    const previousSession = sessions.get(previous.key);
    if (previousSession && !previous.key.startsWith("stable\0")) evict(previousSession);
  }
  let session = sessions.get(key);
  if (!session) {
    session = create(key);
    sessions.set(key, session);
  } else if (session.tabId !== tabId || session.sessionPath !== sessionPath) {
    session.generation += 1;
    session.pendingContent.clear();
    for (const [otherTabId, binding] of bindings) {
      if (binding.key === key) bindings.delete(otherTabId);
    }
    session.tabId = tabId;
    session.sessionPath = sessionPath;
  }
  session.bindingKey = bindingKey;
  bindings.set(tabId, { key, sessionPath, bindingKey });
  touch(session);
  return replaced;
}

export function detachTranscriptTab(
  bindings: Map<string, TranscriptTabBinding>,
  sessions: Map<string, SessionTranscript>,
  tabId: string,
  evict: (session: SessionTranscript) => void,
): void {
  releaseHistoryRead(tabId);
  const binding = bindings.get(tabId);
  bindings.delete(tabId);
  if (binding && !binding.key.startsWith("stable\0")) {
    const session = sessions.get(binding.key);
    if (session) evict(session);
  } else if (!binding) {
    for (const session of Array.from(sessions.values())) {
      if (session.tabId === tabId && !session.key.startsWith("stable\0")) evict(session);
    }
  }
}
