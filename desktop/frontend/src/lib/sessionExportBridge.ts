import { hostEvents } from "./hostEvents";
import type { GeneratedDesktopCommands } from "../generated/desktopContract.generated";
export type SessionExportBindings = Pick<GeneratedDesktopCommands,
  "BeginSessionExportForTarget" | "ReadSessionExportChunk" | "AppendSessionExportPage" | "FinishSessionExport" | "CancelSessionExport">;
export function makeMockSessionExportBindings(): SessionExportBindings {
  const unavailable = async (): Promise<never> => { throw new Error("Session export requires the desktop host."); };
  return {
    BeginSessionExportForTarget: unavailable,
    ReadSessionExportChunk: unavailable,
    AppendSessionExportPage: unavailable,
    FinishSessionExport: unavailable,
    async CancelSessionExport() {},
  };
}

export function onSessionExportProgress(cb: (value: { exportId: string; title: string; phase: string; records: number; pages: number }) => void): () => void {
 return hostEvents("session_export_progress", payload => { if (payload && typeof payload === "object") cb(payload as Parameters<typeof cb>[0]); }) ?? (() => {});
}
