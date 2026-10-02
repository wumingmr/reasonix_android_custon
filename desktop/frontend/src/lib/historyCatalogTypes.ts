import type { SessionMeta } from "./types";
import type { ReadSnapshotPage } from "./readSnapshot";

export interface HistorySessionPageRequest {
  scope: "all" | "project" | "global" | string;
  workspaceRoot: string;
  status: "all" | "current" | "open" | string;
  timeFilter: "all" | "today" | "yesterday" | "older" | string;
  query: string;
  cursor: string;
  limit: number;
}
export interface HistorySessionPage extends ReadSnapshotPage { items: SessionMeta[]; nextCursor: string; revision: number; partial: boolean; staleCursor: boolean }
export interface HistoryIndexStatus {
  state: string; mode: "disk" | "memory" | string; path?: string; revision: number;
  indexed: number; total: number; pending: number; failed: number; lastError?: string; quarantinedPath?: string;
}
export interface HistorySearchRequest {
  query: string; scope: "all" | "project" | "global" | string; workspaceRoot: string;
  status: "all" | "current" | "open" | string; timeFilter: string; kinds: string[]; toolName: string; cursor: string; limit: number;
}
export interface HistorySearchHit {
  partIndex?: number; contentDigest?: string;
  sessionPath: string; sessionId: string; source: string; messageIndex: number; role: string; kind: string;
  toolName?: string; snippet: string; score: number; sessionTitle?: string; topicTitle?: string; workspaceRoot?: string;
  lastActivityAt: number; open: boolean; running: boolean; current: boolean;
}
export interface HistorySearchPage extends ReadSnapshotPage {
  items: HistorySearchHit[]; nextCursor: string; revision: number; partial: boolean; staleCursor: boolean; status: HistoryIndexStatus;
}
export interface HistorySearchContextRequest { sessionPath: string; messageIndex: number; before: number; after: number; contentDigest?: string }
export interface HistorySearchContextLine { index: number; role: string; text: string }
