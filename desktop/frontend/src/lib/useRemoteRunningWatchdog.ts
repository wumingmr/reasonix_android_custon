// The remote running-state watchdog: while the pill claims a turn is running,
// poll the serve's /status and feed it through the shared backend_status
// reducer. This is the remote twin of the local tab's reconcile loop — a lost
// turn_done frame (dropped SSE, slow-consumer drop, half-dead tunnel) then
// clears within one tick instead of spinning forever.

import { useEffect, type RefObject } from "react";

const REMOTE_RUNNING_RECONCILE_MS = 30_000;

/** The connection effect's /status reader, absent while no connection owns the tab. */
export interface RemoteStatusRefreshHandle {
  tabId: string;
  run(): Promise<void>;
}

/**
 * Polls /status while the shown tab claims a running turn and the runtime
 * projection holds no liveness of its own, which is the only case the feed
 * cannot settle by itself.
 */
export function useRemoteRunningWatchdog(input: {
  tabId: string | undefined;
  /** The shown tab adopted a session and hydrated it. */
  ready: boolean;
  /** The runtime projection knows the turn, so its own liveness already settles the pill. */
  runtimeKnown: boolean;
  running: boolean;
  refreshStatusRef: RefObject<RemoteStatusRefreshHandle | null>;
}): void {
  const { tabId, ready, runtimeKnown, running, refreshStatusRef } = input;
  useEffect(() => {
    if (!ready || runtimeKnown || !tabId || !running) return;
    const reconcile = () => {
      const current = refreshStatusRef.current;
      if (!current || current.tabId !== tabId) return;
      void current.run().catch(() => {
        // Transient; the next tick retries.
      });
    };
    const timer = window.setInterval(reconcile, REMOTE_RUNNING_RECONCILE_MS);
    return () => {
      window.clearInterval(timer);
    };
  }, [ready, refreshStatusRef, running, runtimeKnown, tabId]);
}
