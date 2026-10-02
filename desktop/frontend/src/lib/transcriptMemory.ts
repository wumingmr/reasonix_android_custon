import type { Action, State } from "./useController";
import type { SessionTranscript } from "./transcriptStoreTypes";
import type { TranscriptRecord } from "./transcriptRecordProjection";
import { recordBytes } from "./transcriptRecordBytes";

/** Pins retain the live edge, not invisible expanded copies of durable data. */
export function reclaimInvisibleBodies(
  sessions: Iterable<SessionTranscript>, visible: (tabId: string) => boolean,
  budget: number, total: number, publish: (session: SessionTranscript, record: TranscriptRecord) => void,
): number {
  for (const session of sessions) {
    if (total <= budget) break;
    if (visible(session.tabId)) continue;
    for (const rec of session.records) {
      if (total <= budget) break;
      if (!rec.previewMessage) continue;
      const before = rec.bytes;
      rec.message = rec.previewMessage;
      rec.previewMessage = undefined;
      rec.resolved = undefined;
      rec.bytes = recordBytes(rec.message);
      session.bodyBytes += rec.bytes - before;
      total += rec.bytes - before;
      publish(session, rec);
    }
  }
  return total;
}

export function releaseCachedHistory(s: State): State {
  return { ...s, items: s.items.slice(s.historyPrefixCount), historyPrefixCount: 0,
    hydratePlaceholderItems: undefined, hydrateHistoryLoaded: false,
    historyRevision: undefined, historyDigest: undefined, transcriptItemOrder: {},
    offscreenItems: undefined, historyStartTurn: 0, historyEndTurn: 0, historyTotalTurns: 0,
    historyHasOlder: false, historyHasNewer: false, historyOlderLoading: false, historyNewerLoading: false,
    historyOlderError: undefined, historyNewerError: undefined,
    historyMutation: { seq: s.historyMutation.seq + 1, kind: "replace" } };
}

export function isIsolatedStreamDelta(action: Action, prev: State, next: State): boolean {
  return (action.type === "stream_batch" ||
    (action.type === "event" && (action.e.kind === "text" || action.e.kind === "reasoning"))) &&
    prev.items === next.items && prev.currentAssistant === next.currentAssistant &&
    prev.pendingUser === next.pendingUser && prev.retry === next.retry;
}
