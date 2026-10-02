// Older servers presented a steer as a derived notice without messageId.
// Recover its owner from the explicit record identity so joining a snapshot
// with canonical history cannot produce another row for the same insertion.
export function historyMessageIdentity(message: { messageId?: string; recordId?: string; role: string; content: string; code?: string }, outerRecordId?: string): string | undefined {
  if (message.messageId) return message.messageId;
  if (message.role !== "notice" || (message.code !== "unapplied_steer" && !message.content.startsWith("↪ "))) return undefined;
  return /^m:(.+):notice:0$/.exec(message.recordId || outerRecordId || "")?.[1];
}

export function createUniqueItemIDAllocator(): (preferred: string, fallback: string) => string {
  const used = new Set<string>();
  return (preferred, fallback) => {
    const base = preferred || fallback;
    if (!used.has(base)) {
      used.add(base);
      return base;
    }
    let suffix = 1;
    while (used.has(`${base}#${suffix}`)) suffix += 1;
    const id = `${base}#${suffix}`;
    used.add(id);
    return id;
  };
}
