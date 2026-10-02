import { resolvedHistoryField } from "./canonicalTranscriptBackend";
import { applyResolvedField, type TranscriptRecord } from "./transcriptRecordProjection";
import { recordBytes } from "./transcriptRecordBytes";
import type { SessionTranscript } from "./transcriptStoreTypes";
import type { HistoryContentChunk, HistoryContentRef } from "./types";

interface ContentReadOwner {
  locate(entryId: string): { session: SessionTranscript; entryId: string } | undefined;
  resident(session: SessionTranscript): boolean;
  read(ref: HistoryContentRef, index: number): Promise<HistoryContentChunk>;
  publish(session: SessionTranscript, record: TranscriptRecord, ref: HistoryContentRef): void;
}

/** Resolve against one resident owner, with one handoff to its replacement cut. */
export async function readTranscriptContent(
  owner: ContentReadOwner, alias: string, field: string,
  previous?: SessionTranscript, handoff = true,
): Promise<string | undefined> {
  const location = owner.locate(alias);
  if (!location || (previous && previous !== location.session)) return undefined;
  const { session, entryId } = location;
  const generation = session.generation;
  const settlement = session.generationSettlement;
  if (settlement?.generation === generation) await settlement.promise;
  if (!owner.resident(session) || generation !== session.generation) return undefined;
  const rec = session.byId.get(entryId);
  if (!rec) return undefined;
  if (rec.resolved?.[field] !== undefined) return rec.resolved[field];
  const ref = rec.refs.find(candidate => candidate.field === field || candidate.field === "canonicalMessage");
  if (!ref) return undefined;
  const pendingKey = JSON.stringify([entryId, field]);
  const pending = session.pendingContent.get(pendingKey);
  if (pending?.generation === generation) return pending.promise;
  const request = (async (): Promise<string | undefined> => {
    let data = "";
    for (let index = 0; index < Math.max(1, ref.chunks); index++) {
      const chunk = await owner.read(ref, index);
      // Closing/evicting is terminal, including a reopened tab with the same ID.
      if (!owner.resident(session)) return undefined;
      if (session.generation !== generation) {
        return handoff ? readTranscriptContent(owner, alias, field, session, false) : undefined;
      }
      if (session.byId.get(entryId) !== rec) return undefined;
      if (chunk.stale) {
        rec.staleRefs = { ...rec.staleRefs, [field]: true };
        return undefined;
      }
      data += chunk.data ?? "";
      if (chunk.done) break;
    }
    const preview = rec.previewMessage ?? rec.message;
    if (!applyResolvedField(rec, ref, data)) return undefined;
    rec.previewMessage = preview;
    const previousBytes = rec.bytes;
    rec.bytes = recordBytes(rec.message) + recordBytes(preview);
    session.bodyBytes += rec.bytes - previousBytes;
    const value = ref.field === "canonicalMessage" ? resolvedHistoryField(rec.message, field) : data;
    if (value === undefined) return undefined;
    rec.resolved = { ...rec.resolved, [field]: value };
    owner.publish(session, rec, ref);
    return value;
  })();
  const entry = { generation, promise: request };
  const release = () => {
    if (session.pendingContent.get(pendingKey) === entry) session.pendingContent.delete(pendingKey);
  };
  session.pendingContent.set(pendingKey, entry);
  void request.then(release, release);
  return request;
}
