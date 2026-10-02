import type { InboxTarget } from "./pendingFollowup";
import type { InboxSnapshotLike } from "./composerInboxQueue";

export type InboxQueueEdit = { id: string; text: string; contentVersion: string; references: string[] };
export type InboxQueueRequest = {
  kind: "snapshot" | "read" | "edit" | "move" | "delete" | "pause" | "retry" | "steer" | "enqueue_steer";
  itemId?: string; text?: string; contentVersion?: string;
  beforeItemId?: string | null; queueRevision?: number; paused?: boolean;
  turnId?: string;
  display?: string; idempotencyKey?: string;
};
export type InboxQueueResult = {
  outcome: "applied" | "unchanged" | "conflict" | "unavailable";
  reason?: string; snapshot: InboxSnapshotLike; edit?: InboxQueueEdit;
  receipt?: { itemId: string; disposition: string; position: number; paused: boolean; error?: string };
};
export interface InboxQueueBindings {
  InboxQueueForTarget?(target: InboxTarget, request: InboxQueueRequest): Promise<InboxQueueResult>;
}

export function queuePending(state?: string): boolean {
  return state === "queued" || state === "blocked" || state === "uncertain";
}

// Adapted from zai-org/ZCode ConversationQueuePanel (Apache-2.0),
// 872ad960. Reasonix uses the same ID anchor for pointer and keyboard moves.
export function queueMoveAnchor(ids: readonly string[], active: string, over: string): string | null | undefined {
  if (active === over) return undefined;
  const from = ids.indexOf(active), to = ids.indexOf(over);
  if (from < 0 || to < 0) return undefined;
  if (from < to) {
    const remaining = ids.filter(id => id !== active);
    return remaining[remaining.indexOf(over) + 1] ?? null;
  }
  return over;
}

export function queueActionAnchor(ids: readonly string[], id: string, action: "up" | "down" | "first" | "last"): string | null | undefined {
  const index = ids.indexOf(id);
  if (index < 0) return undefined;
  if (action === "up") return index === 0 ? undefined : ids[index - 1];
  if (action === "down") return index === ids.length - 1 ? undefined : ids[index + 2] ?? null;
  if (action === "first") return index === 0 ? undefined : ids[0];
  return index === ids.length - 1 ? undefined : null;
}
