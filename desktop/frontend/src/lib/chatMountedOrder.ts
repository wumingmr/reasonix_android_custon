/**
 * The committed subset of a progressively mounted chat order.
 *
 * History data may arrive before its DOM nodes are revealed. Consumers such as
 * the turn navigator subscribe here so every advertised target already exists
 * in the document. This is ephemeral view state and is discarded with the
 * active chat session.
 */
export class ChatMountedOrder {
  private order: readonly string[] = [];
  private listeners = new Set<() => void>();

  getSnapshot = (): readonly string[] => this.order;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  publish(next: readonly string[]): void {
    if (this.order.length === next.length && this.order.every((key, index) => key === next[index])) return;
    this.order = next;
    // A synchronous external-store notification may unsubscribe and resubscribe
    // while React renders. Iterate a snapshot so the new subscription cannot be
    // visited again by the same publication.
    for (const listener of [...this.listeners]) listener();
  }

  dispose(): void {
    this.order = [];
    this.listeners.clear();
  }
}

export const CHAT_HISTORY_MOUNT_BATCH = 24;

export function reconcileMountedOrder(current: readonly string[], order: readonly string[]): readonly string[] {
  if (!current.length) return order;
  const start = order.indexOf(current[0]);
  const contiguous = start >= 0 && current.every((key, index) => order[start + index] === key);
  if (!contiguous) return order;
  const suffix = order.slice(start + current.length);
  return suffix.length ? [...current, ...suffix] : current;
}

/** Add the nearest leading history nodes while keeping the mounted suffix intact. */
export function revealEarlierMountedOrder(current: readonly string[], order: readonly string[]): readonly string[] {
  if (!current.length) return order;
  const start = order.indexOf(current[0]);
  const contiguous = start >= 0 && current.every((key, index) => order[start + index] === key);
  if (!contiguous) return order;
  const chunkStart = Math.max(0, start - CHAT_HISTORY_MOUNT_BATCH);
  const suffix = order.slice(start + current.length);
  if (chunkStart === start && !suffix.length) return current;
  return [...order.slice(chunkStart, start), ...current, ...suffix];
}
