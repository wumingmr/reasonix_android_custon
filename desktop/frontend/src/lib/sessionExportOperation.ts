import { app } from "./bridge";
import { onSessionExportProgress } from "./sessionExportBridge";
import type { SessionExportHandle, SessionSelector } from "../generated/desktopContract.generated";
import { t } from "./i18n";
import { getTranscriptStore } from "./transcriptStore";
import { sessionObservationDiagnostics } from "./sessionObservationDiagnostics";
import { sessionPipelineDiagnostics } from "./sessionDiagnostics";

import { cancelled, publish, remove } from "./sessionExportProgress";
function assertActive(id: string) { if (cancelled.has(id)) throw new DOMException("Export cancelled", "AbortError"); }

export async function* exportChunks(handle: SessionExportHandle): AsyncGenerator<string> {
  let offset = 0;
  const decoder = new TextDecoder("utf-8", { fatal: true });
  for (;;) {
    assertActive(handle.exportId);
    const chunk = await app.ReadSessionExportChunk(handle.exportId, offset);
    const bytes = Uint8Array.from(atob(chunk.data), value => value.charCodeAt(0));
    yield decoder.decode(bytes, { stream: !chunk.done });
    if (chunk.done) return;
    if (chunk.nextOffset <= offset) throw new Error("Export cursor did not advance");
    offset = chunk.nextOffset;
  }
}
export async function* exportBlocks(handle: SessionExportHandle): AsyncGenerator<{ kind: string; text: string; label?: string }> {
  let pending = "";
  for await (const chunk of exportChunks(handle)) {
    pending += chunk;
    for (;;) {
      const end = pending.indexOf("\n"); if (end < 0) break;
      const line = pending.slice(0, end); pending = pending.slice(end + 1);
      if (line) yield JSON.parse(line);
    }
  }
  if (pending.trim()) throw new Error("Incomplete export block stream");
}

export async function runSessionExport(input: {
  selector: SessionSelector; tabId: string; format: string; title: string; remote: boolean;
  residentItems: number; runningStream: boolean; unresolvedTools: number;
}): Promise<{ text?: string; files: number; cancelled: boolean }> {
  let handle: SessionExportHandle | undefined;
  const off = onSessionExportProgress(value => { if (handle?.exportId === value.exportId && !cancelled.has(value.exportId)) publish(value); });
  try {
    const route = input.selector.ref ? `session-id:${input.selector.ref.sessionId}` : input.selector.sessionPath ?? "";
    const observation = JSON.stringify({ binding: getTranscriptStore().exportObservation(input.tabId, route), lifecycleDiagnostics: sessionObservationDiagnostics(route, input.tabId), readDiagnostics: sessionPipelineDiagnostics(), capturedAt: new Date().toISOString(), tabId: input.tabId, remote: input.remote, residentItems: input.residentItems, runningStream: input.runningStream, unresolvedTools: input.unresolvedTools });
    handle = await app.BeginSessionExportForTarget(input.selector, input.tabId, input.format, input.title, observation);
    if (!handle.exportId) return { files: 0, cancelled: true };
    publish({ exportId: handle.exportId, title: input.title, phase: "preparing", records: 0, pages: 0 });
    let text: string | undefined;
    if (input.format === "clipboard") {
      const parts: string[] = [];
      for await (const part of exportChunks(handle)) parts.push(part);
      try { text = parts.join(""); } catch { throw new Error(t("topicBar.exportClipboardTooLarge")); }
    } else if (input.format === "pdf" || input.format === "image") {
      const { renderSessionExportPages, blobToBase64 } = await import("./sessionExport");
      let index = 0;
      for await (const page of renderSessionExportPages(exportBlocks(handle), input.format)) {
        assertActive(handle.exportId);
        // Bound each RPC payload independently of the encoded page size.
        for (let offset = 0; offset < page.blob.size; offset += 1 << 20) {
          const end = Math.min(offset + (1 << 20), page.blob.size);
          await app.AppendSessionExportPage(handle.exportId, { index, offset, data: await blobToBase64(page.blob.slice(offset, end)), done: end === page.blob.size, width: page.width, height: page.height });
        }
        index++;
      }
    }
    assertActive(handle.exportId);
    const result = await app.FinishSessionExport(handle.exportId);
    return { text, files: (result.paths ?? []).length, cancelled: false };
  } catch (error) {
    if (handle?.exportId) await app.CancelSessionExport(handle.exportId).catch(() => {});
    if (handle && cancelled.has(handle.exportId)) return { files: 0, cancelled: true };
    throw error;
  } finally {
    off(); if (handle?.exportId) { remove(handle.exportId); cancelled.delete(handle.exportId); }
  }
}
