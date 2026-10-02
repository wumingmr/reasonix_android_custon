import { useCallback, useSyncExternalStore } from "react";
import type { ControllerLiveStore } from "./useController";

/** Keep streaming telemetry on the live subscription, off the app state tree. */
export function useLiveTurnMetrics(liveStore: ControllerLiveStore | undefined, tabId: string | undefined) {
  const subscribe = useCallback(
    (cb: () => void) => liveStore?.subscribe(tabId, cb) ?? (() => {}),
    [liveStore, tabId],
  );
  const liveOutput = useSyncExternalStore(subscribe, () => liveStore?.getSnapshot(tabId));
  const liveModelActiveAt = useSyncExternalStore(subscribe, () => liveStore?.getModelActiveAt?.(tabId));
  const liveRateOutputQuarters = useSyncExternalStore(subscribe, () => liveStore?.getRateOutputQuarters?.(tabId));
  return { liveOutput, liveModelActiveAt, liveRateOutputQuarters };
}
