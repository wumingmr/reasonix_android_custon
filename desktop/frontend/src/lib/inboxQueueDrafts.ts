import type { InboxQueueEdit } from "./inboxQueueCommands";
import type { InboxTarget } from "./pendingFollowup";

export type QueueEditDraft = InboxQueueEdit & { target: InboxTarget; value: string; reason?: string };
export type QueueDraftState = { edit?: QueueEditDraft; recovered: QueueEditDraft[] };
const empty: QueueDraftState = { recovered: [] };
const states = new Map<string, QueueDraftState>();
const listeners = new Set<() => void>();
const storageKey = (scope: string) => `reasonix.inbox-edit.v1:${scope}`;

// Separate from the main composer draft: switching tasks or closing the edit
// view cannot overwrite either piece of user input. No requests are replayed.
export const inboxQueueDrafts = {
  subscribe(fn: () => void) { listeners.add(fn); return () => { listeners.delete(fn); }; },
  get(scope: string): QueueDraftState {
    if (!scope) return empty;
    if (!states.has(scope)) {
      let state = empty;
      try {
        const parsed = JSON.parse(sessionStorage.getItem(storageKey(scope)) || "null");
        if (parsed && Array.isArray(parsed.recovered) && (!parsed.edit || (typeof parsed.edit.value === "string" && parsed.edit.target))) state = parsed;
      } catch { /* The in-memory draft remains available when storage is disabled. */ }
      states.set(scope, state);
    }
    return states.get(scope)!;
  },
  set(scope: string, state: QueueDraftState) {
    if (!scope) return;
    states.set(scope, state);
    try { sessionStorage.setItem(storageKey(scope), JSON.stringify(state)); } catch { /* Keep the live draft. */ }
    listeners.forEach(fn => fn());
  },
};
