// Browser mock uses the same immutable-page contract as the host. The cache
// contains fixture data only and is bounded across all mock list entry points.
const snapshots = new Map<string, { binding: string; rows: unknown[]; expires: number }>();
let sequence = 0;

export function releaseMockReadSnapshot(id: string): void { snapshots.delete(id); }

export function mockReadSnapshotPage<T>(kind: string, query: unknown, cursor: string | undefined, limit: number | undefined, currentRows: T[]) {
  const binding = JSON.stringify([kind, query]);
  let id: string;
  let offset = 0;
  if (cursor) {
    const match = /^mock-read-(\d+):(\d+)$/.exec(cursor);
    if (!match) throw Object.assign(new Error("Read snapshot expired"), { code: "stale_cursor" });
    id = `mock-read-${match[1]}`; offset = Number(match[2]);
  } else {
    for (const [key, value] of snapshots) if (value.expires <= Date.now()) snapshots.delete(key);
    if (snapshots.size >= 64) snapshots.delete(snapshots.keys().next().value!);
    id = `mock-read-${++sequence}`;
    snapshots.set(id, { binding, rows: structuredClone(currentRows), expires: Date.now() + 30 * 60_000 });
  }
  const snapshot = snapshots.get(id);
  if (!snapshot || snapshot.binding !== binding || snapshot.expires <= Date.now() || offset > snapshot.rows.length) throw Object.assign(new Error("Read snapshot expired"), { code: "stale_cursor" });
  const end = Math.min(snapshot.rows.length, offset + Math.min(200, Math.max(1, limit || 50)));
  return { items: structuredClone(snapshot.rows.slice(offset, end)) as T[], nextCursor: end < snapshot.rows.length ? `${id}:${end}` : "", snapshotId: id, snapshotExpiresAt: snapshot.expires };
}
