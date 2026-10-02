import { app } from "./bridge";
import type { HistoryWindowPage, HistoryOutlinePage, HistoryOutlineRequest, SearchHistoryPage } from "../generated/desktopContract.generated";
import type { HistoryWindowRequestView, HistoryWindowPageView } from "./types";
import { supportsHistoryRead, acquireHistoryRead, currentHistoryReadBinding, releaseHistoryRead } from "./historyReadScope";
export { releaseHistoryRead } from "./historyReadScope";

const stale = (): HistoryWindowPageView => ({ entries: [], status: "stale_cursor", olderCursor: "", newerCursor: "",
  hasOlder: false, hasNewer: false, totalTurns: 0, startTurn: 0, endTurn: 0, revision: 0, revisionKnown: false, digest: "" });

export async function readBoundHistoryWindow(tabId: string, req: HistoryWindowRequestView): Promise<HistoryWindowPage | HistoryWindowPageView | undefined> {
  if (!supportsHistoryRead()) return undefined;
  const binding = acquireHistoryRead(tabId);
  const handle = await binding.promise;
  if (currentHistoryReadBinding(tabId) !== binding) return stale();
  if (!handle.capabilities?.includes("history-read-binding-v1")) return undefined;
  if (handle.storageBackend === "canonical") {
    const page = await app.ReadSessionHistoryWindow(handle.id, req);
    if (currentHistoryReadBinding(tabId) !== binding) return stale();
    if (page.status === "stale_cursor") releaseHistoryRead(tabId);
    return page;
  }
  if (req.anchor && !["newest", "cursor"].includes(req.anchor) && !handle.capabilities?.includes("history-native-navigation-v1")) {
    return { ...stale(), status: "unsupported" };
  }
  const result = await app.ReadSessionHistorySlice(handle.id, { cursor: req.cursor ?? "", turns: req.limit ?? 32, entries: req.limit ?? 32, bytes: 1 << 20, newer: req.direction === "newer",
    anchor: req.anchor, turn: req.turn, messageId: req.messageId, generation: req.generation, snapshotSequence: req.snapshotSequence });
  if (currentHistoryReadBinding(tabId) !== binding) return stale();
  if (result.status === "stale_cursor") releaseHistoryRead(tabId);
  const page = result.page;
  return { entries: page.entries ?? [], status: result.status as HistoryWindowPageView["status"], olderCursor: page.nextCursor ?? "", newerCursor: page.newerCursor ?? "",
    hasOlder: page.hasOlder, hasNewer: page.hasNewer ?? false, totalTurns: page.totalTurns, startTurn: page.startTurn, endTurn: page.endTurn,
    revision: page.revision, revisionKnown: page.revisionKnown ?? false, digest: page.digest ?? "" };
}

export async function readBoundHistoryOutline(tabId: string, req: HistoryOutlineRequest): Promise<HistoryOutlinePage | undefined> {
  if (!supportsHistoryRead() || typeof app.ReadSessionHistoryOutline !== "function") return undefined;
  const binding = acquireHistoryRead(tabId);
  const handle = await binding.promise;
  if (currentHistoryReadBinding(tabId) !== binding) return { entries: [], status: "stale_cursor", totalTurns: 0, nextTurn: 0, done: true, snapshotSequence: 0, coverageSequence: 0, generation: "" };
  if (!handle.capabilities?.includes("history-read-binding-v1")) return undefined;
  const page = await app.ReadSessionHistoryOutline(handle.id, req);
  if (currentHistoryReadBinding(tabId) !== binding) return { ...page, entries: [], status: "stale_cursor" };
  if (page.status === "stale_cursor") releaseHistoryRead(tabId);
  return page;
}

export async function searchBoundHistory(tabId: string, text: string, cursor = "", limit = 50): Promise<SearchHistoryPage | undefined> {
  if (!supportsHistoryRead() || typeof app.SearchSessionHistoryRead !== "function") return undefined;
  const empty = (status: string): SearchHistoryPage => ({ hits: [], status, hasMore: false, snapshotSequence: 0, coverageSequence: 0 });
  const binding = acquireHistoryRead(tabId);
  const handle = await binding.promise;
  if (currentHistoryReadBinding(tabId) !== binding) return empty("stale_cursor");
  if (!handle.capabilities?.includes("history-read-binding-v1")) return undefined;
  if (handle.storageBackend !== "canonical" && !handle.capabilities?.includes("history-native-search-v1")) return empty("unsupported");
  const page = await app.SearchSessionHistoryRead(handle.id, text, cursor, limit);
  if (currentHistoryReadBinding(tabId) !== binding) return empty("stale_cursor");
  if (page.status === "stale_cursor") releaseHistoryRead(tabId);
  return page;
}
