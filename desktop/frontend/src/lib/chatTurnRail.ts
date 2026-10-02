import type { TranscriptOutlineEntry } from "./transcriptProtocol";

/** The identities a loaded user turn exposes to the rail. */
export interface LoadedTurnNode {
  /** The item's own id, which is also its DOM anchor key. */
  readonly id: string;
  readonly messageId?: string;
}

/** Item keys the transcript gives a history record that has no message id. */
const RECORD_ITEM_PREFIX = "record:";

/**
 * The transcript record identity behind a loaded user item, or undefined when
 * it does not have one yet.
 *
 * `historyItems` and `transcriptStore` key a history record without a message
 * id as `record:<recordId>` while the outline carries the bare `<recordId>`
 * (`m:<messageId>` for everything the snapshot protocol emitted). Comparing the
 * two item keys directly therefore never matches for older history, which is
 * why this conversion lives here rather than being re-derived at each call
 * site.
 */
export function recordIdOf(item: LoadedTurnNode): string | undefined {
  if (item.id.startsWith(RECORD_ITEM_PREFIX)) return item.id.slice(RECORD_ITEM_PREFIX.length);
  // A committed row already carries the outline's own form.
  if (item.id.startsWith("m:")) return item.id;
  if (item.messageId) return `m:${item.messageId}`;
  // `he:<entryId>` belongs to the windowed legacy store, whose ids live in a
  // different key space and which never has an outline: a tab on that path
  // never installs a snapshot, so the rail stays in its loaded-turn mode.
  // Reporting no identity is correct — guessing one could match the wrong turn.
  return undefined;
}

/** Mounted user turns indexed by both stable identities they can be found by. */
export interface LoadedTurnIndex {
  readonly byMessageId: ReadonlyMap<string, string>;
  readonly byRecordId: ReadonlyMap<string, string>;
}

/**
 * One pass over the mounted order. The merge must not scan the mounted set per
 * outline entry: at ten thousand turns against tens of thousands of nodes that
 * becomes the dominant cost of a render.
 */
export function indexLoadedTurns(order: readonly string[], read: (key: string) => LoadedTurnNode | undefined): LoadedTurnIndex {
  const byMessageId = new Map<string, string>();
  const byRecordId = new Map<string, string>();
  for (const key of order) {
    const node = read(key);
    if (node === undefined) continue;
    if (node.messageId !== undefined && !byMessageId.has(node.messageId)) byMessageId.set(node.messageId, key);
    const recordId = recordIdOf(node);
    if (recordId !== undefined && !byRecordId.has(recordId)) byRecordId.set(recordId, key);
  }
  return { byMessageId, byRecordId };
}

/**
 * The mounted node that answers for one outline entry, or undefined while that
 * turn is still unloaded.
 *
 * Matches on identity, never on the shape of the anchor key: a question
 * submitted in this app session keeps its optimistic `u<seq>` id after the
 * authoritative message settles and only gains a `messageId`. A message ID is
 * the identity that survives settlement, so it wins; the record ID is the
 * stable fallback that also covers a question that has not been committed yet.
 *
 * Shared by the rail and by the jump transaction so both agree on when a target
 * has really mounted.
 */
export function findLoadedTurn(index: LoadedTurnIndex, entry: TranscriptOutlineEntry): string | undefined {
  if (entry.messageId !== undefined) {
    const byMessage = index.byMessageId.get(entry.messageId);
    if (byMessage !== undefined) return byMessage;
  }
  return index.byRecordId.get(entry.id);
}

/** Ordered, de-duplicated outline entries. */
export function alignOutlineEntries(entries: readonly TranscriptOutlineEntry[]): TranscriptOutlineEntry[] {
  const seen = new Set<string>();
  const aligned: TranscriptOutlineEntry[] = [];
  for (const entry of entries) {
    if (seen.has(entry.id)) continue;
    seen.add(entry.id);
    aligned.push(entry);
  }
  aligned.sort((left, right) => left.order - right.order);
  return aligned;
}
