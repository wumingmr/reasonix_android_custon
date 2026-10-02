import { useEffect } from "react";
import { useCommittedCommand } from "../lib/useCommittedCommand";

// The subscription lives with the application runtime, not the lazy panel.
// Only a newly-created tab in the foreground turn may reveal the panel.
export function useBrowserFirstOpen(taskId: string | undefined, turnId: string | undefined, reveal: () => void, sessionId?: string) {
  const current = useCommittedCommand(() => ({ taskId, turnId, sessionId, reveal }));
  useEffect(() => {
    let closed = false, off: (() => void) | undefined;
    void import("./browserFirstOpenLifecycle").then(module => { if (!closed) off = module.observeFirstBrowserOpen(current); });
    return () => { closed = true; off?.(); };
  }, [current]);
}
