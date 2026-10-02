import type { Item } from "./useController";
import type { HistoryContentRef } from "./types";
import type { TranscriptBackend, SessionTranscript } from "./transcriptStoreTypes";
import { itemIdForToolCall, toolCallKey } from "./transcriptRecordProjection";
import { fileDiffFromWire } from "./tools";
type ToolContentOwner = { sessions: Map<string, SessionTranscript>; backend: TranscriptBackend; requestFullContent(tabId: string, entryId: string, field: string): Promise<string | undefined> };
export async function readTranscriptToolContent(owner: ToolContentOwner, tabId: string, item: Extract<Item, { kind: "tool" }>, value: Record<string, unknown>): Promise<string | undefined> {
    const session = [...owner.sessions.values()].find(session => session.tabId === tabId &&
      (item.sourceEntryId ? session.byId.has(item.sourceEntryId)
        : [...session.contributions.values()].some(items => items.some(candidate => candidate.id === item.id))));
    if (!session) return undefined;
    const entryId = item.sourceEntryId
      ?? [...session.contributions].find(([, items]) => items.some(candidate => candidate.id === item.id))?.[0];
    const record = entryId && session.byId.get(entryId);
    if (!record) return undefined;
    let calls = record.message.toolCalls ?? [];
    const matchesItem = (call: (typeof calls)[number], index: number) =>
      session.toolCallDisplayIds.get(toolCallKey(record.entryId, index))
        === item.id || itemIdForToolCall(call.id, `he:${record.entryId}:tc${index}`) === item.id;
    let callIndex = calls.findIndex(matchesItem);
    let call = calls[callIndex];
    let resultId = session.matchTables.get(record.entryId)?.get(callIndex);
    let result = resultId ? session.byId.get(resultId) : record.message.role === "tool" ? record : undefined;
    if (record.refs.some(ref => ref.field === "canonicalMessage")) {
      await owner.requestFullContent(tabId, record.entryId, "content");
      calls = record.message.toolCalls ?? [];
      callIndex = calls.findIndex(matchesItem);
      call = calls[callIndex];
      resultId = session.matchTables.get(record.entryId)?.get(callIndex);
      result = resultId ? session.byId.get(resultId) : record.message.role === "tool" ? record : undefined;
    }
    if (result?.refs.some(ref => ref.field === "canonicalMessage")) {
      await owner.requestFullContent(tabId, result.entryId, "content");
    }
    const generation = session.generation;
    const refs = [
      ...record.refs.filter(ref => call && ref.toolCallId === call.id && (ref.field === "toolArguments" || ref.field === "toolDiff")),
      ...(result?.refs.filter(ref => ref.field === "content" || ref.field === "toolResultError") ?? []),
    ];
    if (refs.some(ref => ref.field === "toolArguments" || ref.field === "toolDiff") && !call?.id && calls.filter(call => !call.id).length > 1) throw new Error("Ambiguous legacy tool reference");
    const full: Record<string, unknown> = { ...value, execution: result?.message.execution ?? value.execution };
    if (call) full.args = call.arguments;
    if (!result && call?.resultObservation?.contentRef) {
      const contentRef = call.resultObservation.contentRef;
      const ref: HistoryContentRef = { entryId: `m:${call.resultObservation.messageId}`, field: "canonicalMessage", size: contentRef.bytes, chunks: Math.ceil(contentRef.bytes / (1 << 20)), canonicalRef: contentRef, revision: session.revision, revKnown: true, digest: contentRef.digest };
      let bytes = "";
      for (let index = 0; index < ref.chunks; index++) {
        const chunk = await owner.backend.HistoryContentForTab(tabId, ref, index);
        if (owner.sessions.get(session.key) !== session || generation !== session.generation || chunk.stale) throw new Error("Tool reference expired; retry");
        bytes += chunk.data ?? "";
      }
      if (bytes.length !== contentRef.bytes) throw new Error("Incomplete tool result content");
      const decoded = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(bytes, char => char.charCodeAt(0))));
      full.output = decoded.raw_content || decoded.content || "";
      full.execution = decoded.tool_execution ?? full.execution;
    }

    for (const ref of refs) {
      let data = "";
      for (let index = 0; index < Math.max(1, ref.chunks); index++) {
        const chunk = await owner.backend.HistoryContentForTab(tabId, ref, index);
        if (owner.sessions.get(session.key) !== session || generation !== session.generation || chunk.stale) throw new Error("Tool reference expired; retry");
        data += chunk.data ?? "";
        if (chunk.done) break;
      }
      if (new TextEncoder().encode(data).byteLength !== ref.size) throw new Error("Incomplete tool content");
      if (ref.field === "toolArguments") full.args = data;
      else if (ref.field === "content") full.output = data;
      else if (ref.field === "toolResultError") full.error = data;
      else full.diff = call ? fileDiffFromWire({ ...call, diff: data }) ?? data : data;
    }
    return JSON.stringify(full, null, 2);
}
