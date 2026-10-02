import { app } from "./bridge";
import { historyMessageIdentity } from "./historyItemIds";
import type { TranscriptSnapshot } from "./transcriptProtocol";
import type { HistoryEntry, HistoryWindowPageView, HistoryWindowRequestView } from "./types";
import type { HistoryOutlinePage, HistoryOutlineRequest } from "../generated/desktopContract.generated";

type Binding = { snapshotId: string; total: number; sequence: number; remote: boolean };
const bindings = new Map<string, Binding>();
type Cursor = { snapshotId: string; boundary: number; direction: "older" | "newer" };
const cursor = (snapshotId: string, boundary: number, direction: Cursor["direction"]) => JSON.stringify({ snapshotId, boundary, direction });
const staleWindow = (): HistoryWindowPageView => ({ entries: [], status: "stale_cursor", olderCursor: "", newerCursor: "", hasOlder: false, hasNewer: false,
  totalTurns: 0, startTurn: 0, endTurn: 0, revision: 0, revisionKnown: false, digest: "" });

export function bindNativeTranscriptHistory(tabId: string, snapshot: TranscriptSnapshot, remote: boolean): () => void {
  const binding = { snapshotId: snapshot.snapshotId, total: snapshot.totalRecords, sequence: snapshot.coveredThroughSeq, remote };
  bindings.set(tabId, binding);
  return () => { if (bindings.get(tabId) === binding) bindings.delete(tabId); };
}

export function nativeSnapshotWindow(snapshot: TranscriptSnapshot): HistoryWindowPageView {
  const entries: HistoryEntry[] = snapshot.records.map(record => {
    const messageId = historyMessageIdentity(record.message, record.id);
    const entryId = messageId ? `m:${messageId}` : record.message.recordId ?? record.id;
    return { entryId, order: record.order, turn: record.message.historyTurn ?? 0, message: { ...record.message, messageId, recordId: entryId },
      refs: record.refs.map(ref => ({ entryId, field: ref.path[0], size: ref.bytes, chunks: 1,
        revision: snapshot.coveredThroughSeq, revKnown: true, digest: snapshot.snapshotId, transcriptRef: ref })) };
  });
  const end = entries.length ? entries[entries.length - 1].order + 1 : snapshot.before;
  const turns = entries.map(entry => entry.turn).filter(turn => turn > 0);
  return { entries, status: snapshot.stale ? "stale_cursor" : snapshot.notFound ? "not_found" : "ready",
    olderCursor: snapshot.hasOlder ? cursor(snapshot.snapshotId, snapshot.before, "older") : "",
    newerCursor: end < snapshot.totalRecords ? cursor(snapshot.snapshotId, end, "newer") : "",
    hasOlder: snapshot.hasOlder, hasNewer: end < snapshot.totalRecords, totalTurns: snapshot.totalTurns,
    startTurn: turns.length ? Math.min(...turns) : 0, endTurn: turns.length ? Math.max(...turns) : 0,
    revision: snapshot.coveredThroughSeq, revisionKnown: true, digest: snapshot.snapshotId };
}

// Select the reader from the negotiated binding, never from a failed request.
export async function readNativeTranscriptWindow(tabId: string, req: HistoryWindowRequestView): Promise<HistoryWindowPageView | undefined> {
  const binding = bindings.get(tabId);
  if (!binding) return undefined;
  const read = binding.remote ? app.RemoteTranscriptPageForTab : app.TranscriptPageForTab;
  if (!read) throw new Error("Native transcript paging is unavailable");
  const limit = Math.max(1, Math.min(req.limit ?? 32, 100));
  if (!req.cursor && (!req.anchor || req.anchor === "newest")) {
    const snapshot = await read(tabId, { records: limit, ...(req.generation ? { snapshotId: req.generation } : {}) });
    if (bindings.get(tabId) !== binding) return staleWindow();
    if (!req.generation && !snapshot.stale) {
      binding.snapshotId = snapshot.snapshotId; binding.total = snapshot.totalRecords; binding.sequence = snapshot.coveredThroughSeq;
    }
    return nativeSnapshotWindow(snapshot);
  }
  let snapshotId = req.generation || binding.snapshotId, boundary = binding.total;
  let direction: Cursor["direction"] = req.direction === "newer" ? "newer" : "older";
  if (req.cursor) {
    let parsed: Cursor;
    try { parsed = JSON.parse(req.cursor); } catch { return staleWindow(); }
    if (!parsed || typeof parsed.snapshotId !== "string" || !Number.isSafeInteger(parsed.boundary) || parsed.boundary < 0 || !["older", "newer"].includes(parsed.direction)) return staleWindow();
    snapshotId = parsed.snapshotId; boundary = parsed.boundary; direction = parsed.direction;
    if (req.generation && req.generation !== snapshotId) return staleWindow();
  } else if (req.anchor === "turn") {
    const outline = binding.remote ? app.RemoteTranscriptOutlineForTab : app.TranscriptOutlineForTab;
    if (!outline) throw new Error("Native transcript outline is unavailable");
    const page = await outline(tabId, { snapshotId, offset: Math.max(0, (req.turn ?? 1) - 1), entries: 1 });
    if (bindings.get(tabId) !== binding) return staleWindow();
    if (page.stale || !page.entries.length) {
      const view = nativeSnapshotWindow(await read(tabId, { snapshotId, before: 0, records: 1 }));
      if (bindings.get(tabId) !== binding) return staleWindow();
      return { ...view, status: page.stale ? "stale_cursor" : "not_found" };
    }
    boundary = page.entries[0].order; direction = "newer";
  } else if (req.anchor === "message") {
    const snapshot = await read(tabId, { snapshotId, messageId: req.messageId, records: limit });
    return { ...nativeSnapshotWindow(snapshot), ...(bindings.get(tabId) !== binding ? { status: "stale_cursor" as const } : {}) };
  }
  let snapshot = await read(tabId, { snapshotId, before: direction === "newer" ? boundary + limit : boundary, records: limit });
  if (direction === "newer" && !snapshot.stale && snapshot.records.length && snapshot.records[0].order > boundary) {
    // Byte limits may shorten a backwards page. Never skip the forward prefix.
    snapshot = await read(tabId, { snapshotId, before: boundary + 1, records: 1 });
  }
  if (direction === "newer" && !snapshot.stale) {
    // The server clamps before to the cut's end. Its backwards window can
    // then overlap the preceding page; keep only the requested suffix.
    const records = snapshot.records.filter(record => record.order >= boundary);
    snapshot = { ...snapshot, records, before: records[0]?.order ?? Math.min(boundary, snapshot.totalRecords),
      hasOlder: (records[0]?.order ?? boundary) > 0 };
  }
  if (bindings.get(tabId) !== binding) return { ...nativeSnapshotWindow(snapshot), status: "stale_cursor" };
  return nativeSnapshotWindow(snapshot);
}

export async function readNativeTranscriptOutline(tabId: string, req: HistoryOutlineRequest): Promise<HistoryOutlinePage | undefined> {
  const binding = bindings.get(tabId);
  if (!binding) return undefined;
  const read = binding.remote ? app.RemoteTranscriptOutlineForTab : app.TranscriptOutlineForTab;
  if (!read) throw new Error("Native transcript outline is unavailable");
  if (!req.generation) await readNativeTranscriptWindow(tabId, { anchor: "newest", limit: 1 });
  const snapshotId = req.generation || binding.snapshotId;
  const page = await read(tabId, { snapshotId, offset: Math.max(0, (req.startTurn ?? 1) - 1), entries: Math.min(req.limit ?? 128, 1000) });
  return { status: page.stale || bindings.get(tabId) !== binding ? "stale_cursor" : "ready", generation: snapshotId,
    snapshotSequence: req.snapshotSequence ?? binding.sequence, coverageSequence: binding.sequence, totalTurns: page.total,
    entries: page.entries.map(entry => ({ messageId: entry.messageId ?? entry.id, turn: entry.turn, position: entry.order, prompt: entry.prompt, answer: entry.answer })),
    nextTurn: page.nextOffset + 1, done: page.done };
}
