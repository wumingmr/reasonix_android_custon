import { desktopHost } from "./desktopHost";
import { releaseMockReadSnapshot } from "./mockReadSnapshot";

export type ReadSnapshotPage = {
  snapshotId?: string;
  snapshotExpiresAt?: number;
  staleCursor?: boolean;
  readError?: { code: string; reason: string; message: string };
};

// Older hosts encode SessionOperationError in the message; keep that protocol
// compatibility at the bridge boundary rather than in individual components.
export function isStaleRead(error: unknown): boolean {
  const value = error as { code?: string; data?: { sessionCode?: string }; message?: string };
  return value?.code === "stale_cursor" || value?.data?.sessionCode === "stale_cursor"
    || String(value?.message ?? error).includes("stale_cursor");
}

export function assertReadPage(page: ReadSnapshotPage): void {
  if (page.staleCursor) throw Object.assign(new Error(page.readError?.message ?? "Read snapshot expired"), { code: "stale_cursor" });
  if (page.readError) throw Object.assign(new Error(page.readError.message), { code: page.readError.code });
}

export function releaseReadSnapshot(id?: string): void {
  if (!id) return;
  const host = desktopHost();
  if (host.kind !== "none") void host.app?.ReleaseReadSnapshot?.(id).catch(() => {});
  else releaseMockReadSnapshot(id);
}
