import { isHostRecoveryGuidance } from "./hostRecoverySteer";
import type { WireEvent } from "./types";
import type { State } from "./useController";

// A steer receipt acknowledges the inbox item independently of its display
// row. Canonical records may arrive first, and history must never acknowledge
// another pending item merely because it has the same text.
export function applySteerEvent(state: State, event: WireEvent): State {
  const text = event.text ?? "";
  if (isHostRecoveryGuidance(text)) return state;
  const id = event.messageId ? `he:m:${event.messageId}` : `s${state.seq}`;
  const receipt = { key: event.itemId || event.messageId || id, itemId: event.itemId, text };
  const next = { ...state, guidanceConsumed: receipt };
  // Older servers omit messageId. On transcript-v2 the canonical record still
  // owns display; an uncorrelated receipt cannot create a second visible row.
  if (state.transcriptProtocol === 2 && !event.messageId) return { ...next, seq: state.seq + 1 };
  if (state.items.some(item => item.id === id)) return next;
  return { ...next, seq: state.seq + 1, items: [...state.items, {
    kind: "notice" as const, id, level: "info" as const, text: `↪ ${text}`, inboxItemId: event.itemId,
  }] };
}
