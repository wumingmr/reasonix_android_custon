import type { Item } from "./useController";

/** Defensive deduplication uses durable identity, never submission identity. */
export function uniqueUserItems(items: readonly Item[]): Item[] {
  const ids = new Map<string, number>();
  const output: Item[] = [];
  for (const item of items) {
    if (item.kind !== "user") { output.push(item); continue; }
    const identity = item.messageId ? `m:${item.messageId}` : item.id;
    let index = ids.get(identity);
    if (index !== undefined) {
      const prior = output[index];
      if (prior.kind === "user" && item.messageId) output[index] = { ...prior, ...item };
    } else { index = output.length; output.push(item); }
    ids.set(identity, index);
  }
  return output;
}
