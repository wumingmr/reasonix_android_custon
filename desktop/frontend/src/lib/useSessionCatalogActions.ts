import { useCallback } from "react";
import { app } from "./bridge";
import { asArray } from "./array";
import type { HistoryMessage } from "./types";
import type { SessionRef } from "./sessionRef";

export function useSessionCatalogActions(
  requireIntent: (seq: number) => Promise<void>,
  current: (seq: number) => boolean,
  sync: (reset: boolean, guard: boolean, options: { navigationIntentSeq: number; surfacePolicy: "replace-surface" }) => Promise<string | undefined>,
  invalidateCache: () => void,
) {
  const openCanonicalSession = useCallback(async (ref: SessionRef, navigationIntentSeq: number): Promise<void> => {
    await requireIntent(navigationIntentSeq);
    if (!current(navigationIntentSeq)) return;
    await app.OpenSession(ref);
    if (!current(navigationIntentSeq)) return;
    // User navigation adopts the active surface; background ready events cannot.
    await sync(true, false, { navigationIntentSeq, surfacePolicy: "replace-surface" });
  }, [requireIntent, current, sync]);
  const previewSession = useCallback(async (path: string): Promise<HistoryMessage[]> => asArray<HistoryMessage>(await app.PreviewSession(path).catch(() => [])), []);
  const deleteSession = useCallback((path: string) => app.DeleteSession(path).finally(() => invalidateCache()), [invalidateCache]);
  const restoreSession = useCallback((path: string) => app.RestoreSession(path).finally(() => invalidateCache()), [invalidateCache]);
  const purgeTrashedSession = useCallback((path: string) => app.PurgeTrashedSession(path).finally(() => invalidateCache()), [invalidateCache]);
  const renameSession = useCallback((path: string, title: string) => app.RenameSession(path, title).catch(() => {}).finally(() => invalidateCache()), [invalidateCache]);
  return { openCanonicalSession, previewSession, deleteSession, restoreSession, purgeTrashedSession, renameSession };
}
