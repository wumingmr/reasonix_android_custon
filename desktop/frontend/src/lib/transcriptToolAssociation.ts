import { toolCallKey, type ToolProjectionView, type TranscriptRecord } from "./transcriptRecordProjection";

/** Build the bounded resident window's explicit call/result association. */
export function buildToolProjectionView(records: TranscriptRecord[], stableDisplayIds = new Map<string, string>(),
  stableCallDisplayIds = new Map<string, string>()): ToolProjectionView {
  const indexOf = new Map<string, number>();
  const resultsByMessageId = new Map<string, TranscriptRecord>();
  const observedCallIds = new Map<string, Set<string>>();
  const callsById = new Map<string, Array<{ record: TranscriptRecord; index: number }>>();
  const toolResultOwners = new Map<string, string>();
  const toolCallOwners = new Map<string, string>();
  const toolCallDisplayIds = new Map(stableCallDisplayIds);
  const toolDisplayIds = new Map(stableDisplayIds);
  const toolIdentityConflicts = new Set<string>();
  const suppressedToolResults = new Set<string>();
  const claimedToolResults = new Set<string>();
  records.forEach((record, recordIndex) => {
    indexOf.set(record.entryId, recordIndex);
    if (record.message.role === "tool" && record.message.messageId) {
      const current = resultsByMessageId.get(record.message.messageId);
      if (!current || record.entryId === `m:${record.message.messageId}`) resultsByMessageId.set(record.message.messageId, record);
    }
    if (record.message.role !== "assistant") return;
    (record.message.toolCalls ?? []).forEach((call, callIndex) => {
      if (call.id) {
        const calls = callsById.get(call.id) ?? [];
        calls.push({ record, index: callIndex });
        callsById.set(call.id, calls);
      }
      const messageId = call.resultObservation?.messageId;
      if (!messageId || !call.id) return;
      const ids = observedCallIds.get(messageId) ?? new Set<string>();
      ids.add(call.id);
      observedCallIds.set(messageId, ids);
    });
  });
  const usedCallDisplayIds = new Set(toolCallDisplayIds.values());
  for (const [callId, calls] of callsById) {
    for (const { record, index } of calls) {
      const key = toolCallKey(record.entryId, index);
      if (toolCallDisplayIds.has(key)) continue;
      const displayId = !usedCallDisplayIds.has(callId)
        ? callId
        : `${callId}:call:${encodeURIComponent(record.message.messageId || record.entryId)}:${index}`;
      toolCallDisplayIds.set(key, displayId);
      usedCallDisplayIds.add(displayId);
    }
  }

  const compatible = (call: TranscriptRecord, result: TranscriptRecord): boolean => {
    if (call.message.turnId && result.message.turnId && call.message.turnId !== result.message.turnId) return false;
    return !(call.turn > 0 && result.turn > 0 && call.turn !== result.turn);
  };
  const effectiveCallId = (result: TranscriptRecord): string | undefined => {
    if (result.message.role !== "tool") return undefined;
    if (result.message.toolCallId) return result.message.toolCallId;
    const observed = result.message.messageId ? observedCallIds.get(result.message.messageId) : undefined;
    return observed?.size === 1 ? [...observed][0] : undefined;
  };
  const resultsByCall = new Map<string, TranscriptRecord[]>();
  for (const result of records) {
    const callId = effectiveCallId(result);
    if (!callId) continue;
    const group = resultsByCall.get(callId) ?? [];
    group.push(result);
    resultsByCall.set(callId, group);
  }

  const canonicalResultsByCall = new Map<string, TranscriptRecord[]>();
  for (const [callId, group] of resultsByCall) {
    const formalByMessage = new Map<string, TranscriptRecord>();
    for (const result of group) {
      const messageId = result.message.messageId;
      if (!messageId) continue;
      const current = formalByMessage.get(messageId);
      if (!current) { formalByMessage.set(messageId, result); continue; }
      const preferResult = result.entryId === `m:${messageId}` && current.entryId !== `m:${messageId}`;
      const kept = preferResult ? result : current;
      const duplicate = preferResult ? current : result;
      formalByMessage.set(messageId, kept);
      suppressedToolResults.add(duplicate.entryId);
    }
    const formal = [...formalByMessage.values()];
    const aliases = group.filter(result => !result.message.messageId);
    canonicalResultsByCall.set(callId, [...formal, ...aliases]);
    const calls = callsById.get(callId) ?? [];
    if (formal.length === 1) toolResultOwners.set(callId, formal[0].entryId);
    else if (formal.length === 0 && aliases.length === 1) toolResultOwners.set(callId, aliases[0].entryId);

    for (const alias of formal.length > 0 ? aliases : []) {
      if (formal.some(owner => (!alias.turn || !owner.turn || alias.turn === owner.turn)
        && (!alias.message.turnId || !owner.message.turnId || alias.message.turnId === owner.message.turnId))) {
        suppressedToolResults.add(alias.entryId);
      }
    }

    const ambiguousCalls = calls.filter(({ record, index }) => {
      const observation = record.message.toolCalls?.[index]?.resultObservation?.messageId;
      return !observation && formal.filter(result => compatible(record, result)).length > 1;
    });
    const identityConflict = formal.length > 1 && (calls.length === 0 || ambiguousCalls.length > 0);
    if (identityConflict) {
      for (const result of formal) toolIdentityConflicts.add(result.entryId);
      for (const call of ambiguousCalls) toolIdentityConflicts.add(toolCallKey(call.record.entryId, call.index));
    }

    for (let index = 0; index < formal.length; index += 1) {
      const result = formal[index];
      let displayId = toolDisplayIds.get(result.entryId);
      const callWillOwnBaseId = identityConflict && calls.length > 0;
      if (!displayId || (callWillOwnBaseId && displayId === callId)) {
        displayId = formal.length > 1 && (callWillOwnBaseId || index > 0)
          ? `${callId}:conflict:${encodeURIComponent(result.message.messageId || result.entryId)}`
          : callId;
      }
      toolDisplayIds.set(result.entryId, displayId);
    }
    for (const alias of aliases) if (!toolDisplayIds.has(alias.entryId)) toolDisplayIds.set(alias.entryId, callId);
  }

  for (const [callId, calls] of callsById) {
    const group = canonicalResultsByCall.get(callId) ?? [];
    for (const { record, index } of calls) {
      const call = record.message.toolCalls?.[index];
      if (!call) continue;
      let owner: TranscriptRecord | undefined;
      if (call.resultObservation?.messageId) {
        const explicit = resultsByMessageId.get(call.resultObservation.messageId);
        if (explicit && compatible(record, explicit)) owner = explicit;
      } else {
        const candidates = group.filter(result => compatible(record, result));
        const formal = candidates.filter(result => Boolean(result.message.messageId));
        if (formal.length === 1) owner = formal[0];
        else if (formal.length === 0 && candidates.length === 1) owner = candidates[0];
      }
      if (owner) {
        toolCallOwners.set(toolCallKey(record.entryId, index), owner.entryId);
        claimedToolResults.add(owner.entryId);
      }
    }
  }
  for (const entryId of toolDisplayIds.keys()) if (!indexOf.has(entryId)) toolDisplayIds.delete(entryId);
  const callKeys = new Set([...callsById.values()].flatMap(calls => calls.map(call => toolCallKey(call.record.entryId, call.index))));
  for (const key of toolCallDisplayIds.keys()) if (!callKeys.has(key)) toolCallDisplayIds.delete(key);
  return { records, indexOf, toolResultOwners, toolCallOwners, toolCallDisplayIds, toolDisplayIds,
    toolIdentityConflicts, suppressedToolResults, claimedToolResults };
}
