import { useEffect, useSyncExternalStore } from "react";
import { app } from "./bridge";
import { runtimeStateStore, selectRuntime, selectRuntimeSession } from "./runtimeStateStore";
import type { SessionIdentity } from "./sessionIdentity";

export function useRuntimeSession(tabId: string | undefined, identity: SessionIdentity | string | undefined) {
  const snapshot = useSyncExternalStore(runtimeStateStore.subscribe, runtimeStateStore.getSnapshot);
  const failed = useSyncExternalStore(runtimeStateStore.subscribe, runtimeStateStore.getFailed);
  const session = selectRuntimeSession(snapshot, tabId, identity);
  return selectRuntime(session, failed);
}

export function useRuntimeStateSync() {
  useEffect(() => {
    if (!app.GetRuntimeStateSnapshot) return;
    let disposed = false;
    let stop: (() => void) | undefined;
    void import("./runtimeStateSync").then(({ startAppRuntimeStateSync }) => {
      if (!disposed) stop = startAppRuntimeStateSync();
    });
    return () => { disposed = true; stop?.(); };
  }, []);
}
