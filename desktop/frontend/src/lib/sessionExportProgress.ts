import { app } from "./bridge";
export type ExportProgress = { exportId: string; title: string; phase: string; records: number; pages: number };
let current: readonly ExportProgress[] = [];
const listeners = new Set<() => void>();
export const cancelled = new Set<string>();
export function publish(progress: ExportProgress) {
  current = [...current.filter(item => item.exportId !== progress.exportId), progress];
  listeners.forEach(fn => fn());
}
export function remove(id: string) { current = current.filter(item => item.exportId !== id); listeners.forEach(fn => fn()); }
export const sessionExportProgress = { getSnapshot: () => current, subscribe: (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn); }; } };
export async function cancelSessionExport(id: string) { cancelled.add(id); remove(id); await app.CancelSessionExport(id); }
