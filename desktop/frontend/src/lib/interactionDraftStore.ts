export type InteractionDraftStore<Value> = {
  read(key: string): Value | undefined;
  write(key: string, value: Value): void;
  delete(key: string): void;
  releaseScope(scope: string): void;
  size(): number;
};

/** Request-owned, bounded volatile state. Entries survive a component remount,
 * but cannot become an unbounded process-global cache. */
export function createInteractionDraftStore<Value>(limit = 128): InteractionDraftStore<Value> {
  const entries = new Map<string, { scope: string; value: Value }>();
  const touch = (key: string, entry: { scope: string; value: Value }) => {
    entries.delete(key);
    entries.set(key, entry);
  };
  return {
    read(key) {
      const entry = entries.get(key);
      if (!entry) return undefined;
      touch(key, entry);
      return entry.value;
    },
    write(key, value) {
      const split = key.indexOf("\u0000");
      touch(key, { scope: split < 0 ? key : key.slice(0, split), value });
      while (entries.size > limit) entries.delete(entries.keys().next().value!);
    },
    delete(key) { entries.delete(key); },
    releaseScope(scope) {
      for (const [key, entry] of entries) if (entry.scope === scope) entries.delete(key);
    },
    size: () => entries.size,
  };
}
