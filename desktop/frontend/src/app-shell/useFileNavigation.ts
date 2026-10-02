import { useCallback, useLayoutEffect, useSyncExternalStore } from "react";
import {
  fileNavigationKey,
  type FileNavigationOwner,
  type FileNavigationScope,
  type FileNavigationScopeKey,
  type FileNavigationSnapshot,
} from "../lib/fileNavigationOwner";

/**
 * Read the committed navigation of one dock instance. Reading never starts a
 * navigation: the panel renders what a command already decided, so a remount,
 * a StrictMode replay or a parent re-render cannot open a file on its own.
 */
export function useFileNavigationRecord(
  owner: FileNavigationOwner,
  scope: FileNavigationScope,
  key: FileNavigationScopeKey,
): FileNavigationSnapshot | null {
  const recordKey = fileNavigationKey(scope);
  const subscribe = useCallback((listener: () => void) => owner.subscribe(listener), [owner]);
  const read = useCallback(() => owner.getSnapshot(recordKey), [owner, recordKey]);
  const snapshot = useSyncExternalStore(subscribe, read, read);
  const { sessionTabId, dockTabId } = scope;
  const { resource, session } = key;
  useLayoutEffect(
    () => { owner.bindScope({ sessionTabId, dockTabId }, { resource, session }); },
    [dockTabId, owner, resource, session, sessionTabId],
  );
  return snapshot;
}
