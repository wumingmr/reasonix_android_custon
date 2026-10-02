import { ErrorMessage } from "./ErrorMessage";
import { useCallback, useEffect, useRef, useState } from "react";
import { app, onProjectTreeChanged } from "../lib/bridge";
import { asArray } from "../lib/array";
import { useT } from "../lib/i18n";
import type { RecoveryEntryView, SessionRestoreResult } from "../generated/desktopContract.generated";
import type { HistoryMessage } from "../lib/types";
import type { SessionRef } from "../lib/sessionRef";
import { useManagementT } from "../lib/managementLocale";

export function HistoricalRecoveryList({ active, onOpenSession }: {
  active: boolean;
  onOpenSession?: (ref: SessionRef) => Promise<void>;
}) {
  const t = useT();
  const m = useManagementT();
  const [items, setItems] = useState<RecoveryEntryView[]>([]);
  const [query, setQuery] = useState("");
  const [nextCursor, setNextCursor] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<HistoryMessage[]>([]);
  const [restored, setRestored] = useState<SessionRestoreResult>();
  const [workspaces, setWorkspaces] = useState<Record<string,string>>({});
  const registryGeneration = useRef(0);
  const surfaceGeneration = useRef(0);
  const pendingRequests = useRef(new Map<string, import("../generated/desktopContract.generated").SessionLifecycleRequest>());
  const generation = useRef(0);
  const previewGeneration = useRef(0);
  const mutating = useRef(false);
  const reload = useCallback(async (cursor = "") => {
    const seq = ++generation.current;
    setLoading(true);
    try {
      const page = await app.ListRecoveryEntries(query, cursor, 50);
      if (seq !== generation.current) return;
      const rows = asArray<RecoveryEntryView>(page.items);
      registryGeneration.current = page.generation;
      setNextCursor(page.nextCursor ?? "");
      setItems(current => cursor ? [...current, ...rows.filter(row => !current.some(old => old.id === row.id))] : rows);
      setError("");
    } catch (err) {
      if (seq === generation.current) setError(err instanceof Error ? err.message : String(err));
      throw err;
    } finally {
      if (seq === generation.current) setLoading(false);
    }
  }, [query]);
  useEffect(() => {
    if (!active) return;
    void reload().catch(() => {});
    const unsubscribe = onProjectTreeChanged(() => { if (!mutating.current) void reload().catch(() => {}); });
    return () => { generation.current++; previewGeneration.current++; surfaceGeneration.current++; unsubscribe(); };
  }, [active, reload]);
  const restore = async (entry: RecoveryEntryView) => {
    if (mutating.current) return;
    const surface = surfaceGeneration.current;
    mutating.current = true; setBusy(true);
    const seq = ++generation.current;
    const key = `${entry.id}:${workspaces[entry.id] ?? ""}`;
    try {
      const request = pendingRequests.current.get(key) ?? { operationId: crypto.randomUUID(), action: "restore",
        targets: [{ recoveryEntryId: entry.id, workspaceId: workspaces[entry.id] }], expectedGeneration: registryGeneration.current };
      pendingRequests.current.set(key, request);
      const response = await app.ApplySessionLifecycle(request);
      const item = response.items[0];
      if (item && !item.committed && item.retryable === false) pendingRequests.current.delete(key);
      if (!item?.committed || !item.ref) throw new Error(t("history.failedLoadHistory"));
      const result: SessionRestoreResult = { session: item.ref, workspaceId: item.workspaceId, generation: response.generation };
      if (seq !== generation.current) return;
      setRestored(result);
      setItems(current => current.filter(row => row.id !== entry.id));
      try { await reload(); } catch { setError(t("history.restoredRefreshFailed")); }
      if (surface === surfaceGeneration.current && onOpenSession) {
        try { await onOpenSession(result.session); } catch { setError(t("history.restoredRefreshFailed")); }
      }
    } catch (err) {
      if (String(err).includes("workspace mutation conflicts with persisted state")) {
        pendingRequests.current.delete(key);
        await reload().catch(() => {});
        setError(String(err));
        return;
      }
      if (seq === generation.current) setError(err instanceof Error ? err.message : String(err));
    } finally { mutating.current = false; setBusy(false); }
  };
  const showPreview = async (entry: RecoveryEntryView) => {
    const seq = ++previewGeneration.current;
    try {
      const page = await app.PreviewRecoveryEntry(entry.id);
      if (seq === previewGeneration.current) setPreview(asArray<HistoryMessage>(page.messages));
    } catch (err) {
      if (seq === previewGeneration.current) setError(err instanceof Error ? err.message : String(err));
    }
  };
  return <div className="archived-sessions">
    <p>{m("historicalDescription")}</p>
    <input aria-label={t("history.searchPlaceholder")} placeholder={t("history.searchPlaceholder")} value={query} disabled={busy} onChange={event => setQuery(event.target.value)} />
    <button className="btn btn--small" disabled={busy || loading} onClick={() => void reload().catch(() => {})}>{t("common.retry")}</button>
    {loading && <div role="status">{t("common.loading")}</div>}
    {error && <div role="alert"><ErrorMessage error={error} /></div>}
    {restored && onOpenSession && <button className="btn btn--small" onClick={() => void onOpenSession(restored.session).catch(err => setError(String(err)))}>{t("history.openRestored")}</button>}
    {!loading && !error && items.length === 0 && <p>{m("noHistoricalSessions")}</p>}
    {items.map(entry => <div className="archived-sessions__row" key={entry.id}>
      <span>{entry.title}</span>
      <small>{entry.format} · {entry.status}</small>
      {!entry.canRestore && <span>{t("history.recoveryReview")}</span>}
      {entry.reason === "workspace_conflict" && <select aria-label={t("history.recoveryWorkspace")} value={workspaces[entry.id] ?? ""} disabled={busy}
        onChange={event => setWorkspaces(current => ({ ...current, [entry.id]: event.target.value }))}>
        <option value="">{t("history.recoveryWorkspace")}</option>
        {(entry.workspaceChoices ?? []).map(workspace => <option key={workspace.id} value={workspace.id}>{workspace.title}</option>)}
      </select>}
      <button className="btn btn--small" disabled={busy || !entry.canPreview} onClick={() => void showPreview(entry)}>{t("history.previewRecovery")}</button>
      <button className="btn btn--small" disabled={busy || !entry.canRestore || (entry.reason === "workspace_conflict" && !workspaces[entry.id])} onClick={() => void restore(entry)}>{t("history.restore")}</button>
    </div>)}
    {nextCursor && <button className="btn btn--small" disabled={busy || loading} onClick={() => void reload(nextCursor).catch(() => {})}>{t("projectTree.loadMore")}</button>}
    {preview.length > 0 && <div aria-label={t("history.previewRecovery")}>{preview.map((message, index) => <pre key={message.messageId || message.recordId || index}>{message.content}</pre>)}</div>}
  </div>;
}
