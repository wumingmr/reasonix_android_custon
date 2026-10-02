import { useCallback, useEffect, useRef, useState } from "react";
import { app } from "./bridge";
import { invalidateSharedQuery } from "./queryCoalesce";
import type { ContextPanelInfo } from "./types";

// Overview snapshots have one owner and a trailing refresh: a burst's final
// usage event must eventually reach the panel even if no further event arrives.
export function useContextPanelSnapshot(tabId: string | undefined, sessionGen: number | undefined,
  refreshKey: number | undefined, usageKey: string, usageSeq: number | undefined) {
  const [snapshot, setSnapshot] = useState<{ tabId: string; sessionGen?: number; info: ContextPanelInfo }>();
  const requestSeq = useRef(0);
  const lastRefreshTime = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const refresh = useCallback(async () => {
    if (!tabId) return;
    clearTimeout(timer.current);
    timer.current = undefined;
    const seq = ++requestSeq.current;
    lastRefreshTime.current = Date.now();
    // A usage/session boundary can change data inside the bridge's 200ms
    // coalescing window. A new request must not adopt the previous snapshot.
    invalidateSharedQuery("ContextPanel", [tabId]);
    try {
      const info = await app.ContextPanel(tabId);
      if (requestSeq.current === seq) setSnapshot({ tabId, sessionGen, info });
    } catch {
      /* Keep the last same-session snapshot while the bridge is unavailable. */
    }
  }, [tabId, sessionGen]);

  useEffect(() => {
    void refresh();
    return () => {
      requestSeq.current++;
      clearTimeout(timer.current);
      timer.current = undefined;
    };
  }, [refresh, refreshKey]);

  useEffect(() => {
    if (!usageKey && !usageSeq) return;
    const delay = Math.max(0, 1000 - (Date.now() - lastRefreshTime.current));
    timer.current = setTimeout(() => { void refresh(); }, delay);
    return () => {
      clearTimeout(timer.current);
      timer.current = undefined;
    };
  }, [usageKey, usageSeq, refresh]);

  return tabId && snapshot?.tabId === tabId && snapshot.sessionGen === sessionGen ? snapshot.info : null;
}
