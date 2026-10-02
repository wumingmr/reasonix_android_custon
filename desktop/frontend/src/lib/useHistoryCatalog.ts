import { desktopHost } from "./desktopHost";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { app } from "./bridge";
import { assertReadPage, isStaleRead, releaseReadSnapshot } from "./readSnapshot";
import { loadProjectTreePageWindow } from "./projectTreeWindow";
import type { HistorySearchHit, SessionMeta, HistorySessionPage, HistorySearchPage } from "./types";

export function useHistoryCatalog({ isTrash, suppliedSessions, scope, status, timeFilter, query }: {
  isTrash: boolean; suppliedSessions: SessionMeta[]; scope: string; status: string; timeFilter: string; query: string;
}) {
  const activeSession = suppliedSessions.find((session) => session.current);
  const workspaceRoot = activeSession?.workspaceRoot ?? "";
  const identity = JSON.stringify([isTrash, scope, workspaceRoot, activeSession?.hostId ?? "", activeSession?.sessionId ?? "", activeSession?.path ?? "", status, timeFilter, query.trim()]);
  const emptyView = { identity, sessions: [] as SessionMeta[], hits: [] as HistorySearchHit[], sessionCursor: "", searchCursor: "", partial: false, progress: { indexed: 0, total: 0 }, error: "", loading: false };
  const [view, setView] = useState(emptyView);
  const viewRef = useRef(view); viewRef.current = view;
  const generation = useRef(0);
  const pending = useRef<number | null>(null);
  const refreshPending = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const lastStart = useRef(0);
  const ids = useRef({ sessions: "", search: "" });
  const windowSize = useRef({ sessions: 50, search: 50 });
  const identityRef = useRef(identity);
  const scheduleRef = useRef<() => void>(() => {});

  const fetchPage = useCallback(async (append: boolean, seq: number) => {
    if (seq !== generation.current || identityRef.current !== identity || isTrash) return;
    if (append && viewRef.current.identity !== identity) return;
    if (pending.current === seq) { if (!append) refreshPending.current = true; return; }
    pending.current = seq;
    lastStart.current = Date.now();
    const before = viewRef.current.identity === identity ? viewRef.current : { identity, sessions: [] as SessionMeta[], hits: [] as HistorySearchHit[], sessionCursor: "", searchCursor: "", partial: false, progress: { indexed: 0, total: 0 }, error: "", loading: false };
    viewRef.current = { ...before, loading: true, error: "" };
    setView(viewRef.current);
    const desired = { sessions: append ? before.sessions.length + 50 : windowSize.current.sessions, search: append ? before.hits.length + 50 : windowSize.current.search };
    let page: HistorySessionPage | null = null;
    let body: HistorySearchPage | null = null;
    let replace = !append;
    const releaseStaged = () => {
      if (page?.snapshotId && page.snapshotId !== ids.current.sessions) releaseReadSnapshot(page.snapshotId);
      if (body?.snapshotId && body.snapshotId !== ids.current.search) releaseReadSnapshot(body.snapshotId);
    };
    try {
      for (let attempt = 0; attempt < 2; attempt++) {
        const base = { scope, workspaceRoot, status, timeFilter, query: query.trim() };
        // One generation owns both requests. allSettled also releases the
        // successful sibling if the other request fails.
        const results = await Promise.allSettled([
          replace || before.sessionCursor ? loadProjectTreePageWindow<SessionMeta, HistorySessionPage>(replace ? "" : before.sessionCursor, replace ? desired.sessions : 50, async (cursor, limit) => {
            const result = await app.ListHistorySessions({ ...base, cursor, limit }); assertReadPage(result); return result;
          }, desired.sessions, false) : Promise.resolve(null),
          query.trim() && (replace || before.searchCursor) ? loadProjectTreePageWindow<HistorySearchHit, HistorySearchPage>(replace ? "" : before.searchCursor, replace ? desired.search : 50, async (cursor, limit) => {
            const result = await app.SearchHistoryContent({ ...base, cursor, limit, kinds: [], toolName: "" }); assertReadPage(result); return result;
          }, desired.search, false) : Promise.resolve(null),
        ]);
        page = results[0].status === "fulfilled" ? results[0].value : null;
        body = results[1].status === "fulfilled" ? results[1].value : null;
        const failure = results.find((result) => result.status === "rejected");
        if (!failure || failure.status !== "rejected") break;
        releaseStaged(); page = null; body = null;
        if (attempt === 1 || !isStaleRead(failure.reason)) throw failure.reason;
        replace = true;
      }
      if (seq !== generation.current) { releaseStaged(); return; }
      if (!replace && ((page?.snapshotId && ids.current.sessions && page.snapshotId !== ids.current.sessions) || (body?.snapshotId && ids.current.search && body.snapshotId !== ids.current.search))) throw new Error("Mixed history read snapshots");
      const sessions = replace ? page?.items ?? [] : [...before.sessions, ...(page?.items ?? [])];
      const hits = replace ? body?.items ?? [] : [...before.hits, ...(body?.items ?? [])];
      if (replace) {
        if (ids.current.sessions !== page?.snapshotId) releaseReadSnapshot(ids.current.sessions);
        if (ids.current.search !== body?.snapshotId) releaseReadSnapshot(ids.current.search);
        ids.current = { sessions: page?.snapshotId ?? "", search: body?.snapshotId ?? "" };
      }
      windowSize.current = { sessions: Math.max(50, desired.sessions), search: Math.max(50, desired.search) };
      const next = { identity, sessions, hits, sessionCursor: page?.nextCursor ?? (replace ? "" : before.sessionCursor), searchCursor: body?.nextCursor ?? (replace ? "" : before.searchCursor), partial: Boolean(page?.partial || body?.partial || (!replace && before.partial)), progress: body ? { indexed: body.status.indexed, total: body.status.total } : before.progress, loading: false, error: "" };
      viewRef.current = next; setView(next);
    } catch (error) {
      releaseStaged();
      if (seq === generation.current) setView((current) => ({ ...current, loading: false, error: error instanceof Error ? error.message : String(error) }));
    } finally {
      if (pending.current === seq) pending.current = null;
      if (seq === generation.current && refreshPending.current) { refreshPending.current = false; scheduleRef.current(); }
    }
  }, [identity, isTrash, query, scope, status, timeFilter, workspaceRoot]);

  const schedule = useCallback(() => {
    if (isTrash || desktopHost().kind === "none") return;
    if (pending.current === generation.current) { refreshPending.current = true; return; }
    if (timer.current !== undefined) return;
    timer.current = setTimeout(() => { timer.current = undefined; void fetchPage(false, generation.current); }, Math.max(200, 500 - (Date.now() - lastStart.current)));
  }, [fetchPage, isTrash]);
  scheduleRef.current = schedule;

  useLayoutEffect(() => {
    identityRef.current = identity;
    const seq = ++generation.current;
    releaseReadSnapshot(ids.current.sessions); releaseReadSnapshot(ids.current.search);
    ids.current = { sessions: "", search: "" };
    windowSize.current = { sessions: 50, search: 50 };
    refreshPending.current = false;
    const initial = !isTrash && desktopHost().kind !== "none" ? setTimeout(() => { void fetchPage(false, seq); }, query.trim() ? 200 : 0) : undefined;
    return () => {
      clearTimeout(initial); ++generation.current;
      if (timer.current !== undefined) clearTimeout(timer.current); timer.current = undefined;
    };
  }, [fetchPage, identity, isTrash, query]);

  useEffect(() => {
    const host = desktopHost();
    if (isTrash || host.kind === "none") return;
    return host.events.on("history-index:changed-v1", () => scheduleRef.current());
  }, [isTrash]);

  useEffect(() => () => { releaseReadSnapshot(ids.current.sessions); releaseReadSnapshot(ids.current.search); }, []);
  const loadMore = useCallback(() => { if (viewRef.current.sessionCursor || viewRef.current.searchCursor) void fetchPage(true, generation.current); }, [fetchPage]);
  const visible = view.identity === identity ? view : emptyView;
  return { readIdentity: identity, sessions: isTrash || desktopHost().kind === "none" ? suppliedSessions : visible.sessions, nextCursor: isTrash ? "" : visible.sessionCursor || visible.searchCursor, partial: visible.partial, progress: visible.progress, searchHits: isTrash ? [] : visible.hits, loadMore, error: visible.error, loading: visible.loading, retry: schedule };
}
