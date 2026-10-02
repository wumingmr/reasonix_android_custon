import type {
  HistoryIndexStatus, HistorySearchContextLine, HistorySearchContextRequest, HistorySearchPage,
  HistorySearchRequest, HistorySessionPage, HistorySessionPageRequest,
} from "./historyCatalogTypes";
import type { SessionMeta } from "./types";
import { mockReadSnapshotPage } from "./mockReadSnapshot";

export interface HistoryCatalogBindings {
  ListHistorySessions(req: HistorySessionPageRequest): Promise<HistorySessionPage>;
  SearchHistoryContent(req: HistorySearchRequest): Promise<HistorySearchPage>;
  GetHistorySearchContext(req: HistorySearchContextRequest): Promise<HistorySearchContextLine[]>;
  GetHistoryIndexStatus(): Promise<HistoryIndexStatus>;
  RebuildHistoryIndex(): Promise<void>;
}

export function makeMockHistoryCatalogBindings(sessions: SessionMeta[]): HistoryCatalogBindings {
  const status = async (): Promise<HistoryIndexStatus> => ({
    state: "ready", mode: "memory", revision: 1, indexed: sessions.length, total: sessions.length, pending: 0, failed: 0,
  });
  return {
    async ListHistorySessions(req) {
      const query = req.query.trim().toLowerCase();
      const items = sessions.filter((session) =>
        (req.scope === "all" || (session.scope || "global") === req.scope) &&
        (req.status !== "current" || session.current) && (req.status !== "open" || session.open) &&
        (!query || [session.title, session.preview, session.topicTitle, session.workspaceRoot].some((value) => (value || "").toLowerCase().includes(query))));
      return { ...mockReadSnapshotPage("history-sessions", [req.scope, req.workspaceRoot, req.status, req.timeFilter, query], req.cursor, req.limit, items), revision: 1, partial: false, staleCursor: false };
    },
    async SearchHistoryContent() { return { items: [], nextCursor: "", revision: 1, partial: false, staleCursor: false, status: await status() }; },
    async GetHistorySearchContext() { return []; },
    GetHistoryIndexStatus: status,
    async RebuildHistoryIndex() {},
  };
}
