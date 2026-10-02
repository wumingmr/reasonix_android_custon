import { useEffect, useMemo, useState } from "react";
import { app, type AppBindings } from "./bridge";
import type { Item } from "./useController";

type Tool = Extract<Item, { kind: "tool" }>;
type Data = NonNullable<Awaited<ReturnType<AppBindings["ToolResultForTab"]>>>;
type Source = { item: Tool; tabId: string | undefined };
type Result = { source: Source; data?: Data; failed?: boolean };

/** Full payloads belong to one tab and one immutable tool snapshot. Filtering
 * during render prevents a previous payload from flashing before effect cleanup. */
export function useArchivedToolData(item: Tool, tabId: string | undefined, open: boolean) {
  const source = useMemo(() => ({ item, tabId }), [item, tabId]);
  const [result, setResult] = useState<Result>();
  const [attempt, setAttempt] = useState(0);
  const current = result?.source === source ? result : undefined;
  const data = item.dataArchived ? current?.data ?? null : null;
  useEffect(() => {
    if (!open || !source.item.dataArchived || !source.tabId || data) return;
    let cancelled = false;
    setResult({ source });
    void app.ToolResultForTab(source.tabId, source.item.id).then(value => {
      if (cancelled) return;
      setResult(value && typeof value.args === "string"
        ? { source, data: value }
        : { source, failed: true });
    }).catch(() => {
      if (!cancelled) setResult({ source, failed: true });
    });
    return () => { cancelled = true; };
  }, [source, open, data, attempt]);
  const failed = Boolean(item.dataArchived && (!tabId || current?.failed));
  return {
    data,
    loading: Boolean(open && item.dataArchived && !data && !failed),
    failed,
    retry: () => setAttempt(value => value + 1),
  };
}
