import { app } from "./bridge";
import type { HistoryContentChunk, HistoryContentRef } from "./types";

function decodeBase64Bytes(data: string): Uint8Array {
  const binary = atob(data);
  return Uint8Array.from(binary, character => character.charCodeAt(0));
}

export async function readCanonicalHistoryContent(tabID: string, ref: HistoryContentRef, chunkIndex: number, remote: boolean, recover: () => void): Promise<HistoryContentChunk> {
  if (ref.transcriptRef) {
    const read = remote ? app.RemoteTranscriptContentForTab : app.TranscriptContentForTab;
    if (!read) throw new Error("Transcript v2 content is unavailable");
    let offset = 0, data = "";
    while (true) {
      const chunk = await read(tabID, { ...ref.transcriptRef, offset });
      if (chunk.stale) {
        recover();
        throw new Error("Transcript content snapshot expired; synchronizing, retry after recovery");
      }
      data += chunk.data;
      if (chunk.done) return { entryId: ref.entryId, field: ref.field, chunk: chunkIndex, chunks: 1, data, done: true, stale: false };
      if (chunk.nextOffset <= offset) throw new Error("Transcript content did not advance");
      offset = chunk.nextOffset;
    }
  }
  if (!ref.canonicalRef) return app.HistoryContentForTab(tabID, ref, chunkIndex);
  const offset = chunkIndex * (1 << 20);
  const chunk = remote
    ? await app.RemoteSessionHistoryContentForTab(tabID, ref.canonicalRef, offset)
    : await app.SessionHistoryContentForTab(tabID, ref.canonicalRef, offset);
  const bytes = decodeBase64Bytes(chunk.data ?? "");
  let data = "";
  for (let start = 0; start < bytes.length; start += 0x8000) data += String.fromCharCode(...bytes.subarray(start, start + 0x8000));
  return { entryId: ref.entryId, field: ref.field, chunk: chunkIndex, chunks: ref.chunks, data, done: chunk.done, stale: false };
}
