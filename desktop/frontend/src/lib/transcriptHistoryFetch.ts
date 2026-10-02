import { asArray } from "./array";
import { loadPreparedHistory, type HistoryPreparationWait } from "./historyPreparation";
import { recordBytes } from "./transcriptRecordBytes";
import { noteHistoryPage } from "./sessionDiagnostics";
import type { HistoryEntry, HistorySlice } from "./types";

// Keep preparation pending while preserving the existing content-free metrics.
export async function fetchPreparedHistorySlice(load: () => Promise<HistorySlice>, current: () => boolean, wait?: HistoryPreparationWait): Promise<HistorySlice | undefined> {
  const startedAt = performance.now();
  const slice = await loadPreparedHistory(load, current, wait);
  if (!slice) return undefined;
  if (slice.error?.trim()) throw new Error(slice.error.trim());
  const entries = asArray<HistoryEntry>(slice.entries);
  noteHistoryPage({ entries: entries.length, inlineBytes: entries.reduce((bytes, entry) => bytes + recordBytes(entry.message), 0),
    durationMs: Math.max(0, performance.now() - startedAt), stale: Boolean(slice.stale), source: slice.source ?? "" });
  return slice;
}
