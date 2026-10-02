import type { WireCompletionSummary } from "./types";
export type DockDelivery = { navigationSource?: string; navigationCancellation?: AbortSignal; acceptNavigation?: () => boolean };
/** Reveals one path on the change surface; the file surface has its own owner. */
export type WorkspaceChangeRevealRequest = DockDelivery & { id: number; path: string };
export type WorkspaceVerificationRevealRequest = DockDelivery & { id: number; summary: WireCompletionSummary; tabId: string; turnStartAt: number; currentSummary?: WireCompletionSummary; sessionPath?: string; view?: "changes" | "checks"; initialPath?: string };
export type WorkspaceFileListRequest = DockDelivery & { id: number; paths: string[] };
export type WorkspaceChangeListEntry = { key: string; path: string; meta: string; time: string; detail: string };
export type WorkspaceChangeListRequest = DockDelivery & { id: number; changes: WorkspaceChangeListEntry[] };
