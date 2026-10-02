import { useEffect, useRef } from "react";
import { useCommittedCommand } from "../lib/useCommittedCommand";
import { useT } from "../lib/i18n";
import { useToast } from "../lib/toast";
import type { AttentionChimeEvent } from "../lib/sound";
import type { NotificationOperation, createRuntimeNotifications } from "../lib/runtimeNotifications";

/** Load notification presentation off the initial render path without losing events. */
export function useRuntimeNotifications(activeTabId: string | undefined) {
  const owner = useRef<ReturnType<typeof createRuntimeNotifications> | null>(null);
  const pending = useRef<NotificationOperation[]>([]);
  const t = useT();
  const { showToast } = useToast();
  const readPorts = useCommittedCommand(() => ({ activeTabId, t, showToast }));
  const accept = useCommittedCommand((operation: NotificationOperation) => {
    if (owner.current) owner.current.accept(operation);
    else pending.current.push(operation);
  });
  useEffect(() => {
    let live = true;
    void import("../lib/runtimeNotifications").then(({ createRuntimeNotifications }) => {
      if (!live) return;
      owner.current = createRuntimeNotifications(readPorts);
      for (const operation of pending.current.splice(0)) owner.current.accept(operation);
      owner.current.start();
    });
    return () => {
      live = false;
      owner.current?.dispose();
      owner.current = null;
      pending.current = [];
    };
  }, [readPorts]);
  const handleNotification = useCommittedCommand((event: AttentionChimeEvent & { err?: string; outcome?: string }) => {
    if (event.kind === "ask_request" || event.kind === "approval_request" || event.kind === "turn_done") accept({ event });
  });
  const resetLegacyAttention = useCommittedCommand((resetTabId?: string) => accept({ resetTabId }));
  return { handleNotification, resetLegacyAttention };
}
