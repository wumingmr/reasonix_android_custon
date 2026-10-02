import { mkdirSync, readFileSync, renameSync, writeFileSync, rmSync } from "node:fs";
import { dirname } from "node:path";
import type { BrowserViewport } from "./viewport.js";
import { validateViewport } from "./viewport.js";

export interface FileReference { source: "workspace" | "presented" | "reference"; path: string; toolCallId: string }
export function validFileReference(value: unknown): value is FileReference { const item = value as FileReference | undefined; return !!item && ["workspace", "presented", "reference"].includes(item.source) && typeof item.path === "string" && item.path.length > 0 && item.path.length <= 8192 && typeof item.toolCallId === "string" && item.toolCallId.length <= 512; }
export interface RecoveredTab { id: string; taskId: string; sessionId: string; url: string; title: string; viewport: BrowserViewport | null; fileReference?: FileReference; [key: string]: unknown }
const prohibited = ["grant", "grantId", "webContentsId", "documentToken", "refs", "observationToken", "pendingOperation", "form", "password"];
export function recoverableURL(value: string): boolean {
  if (value.length > 8192) return false;
  try { const url = new URL(value); return ["http:", "https:"].includes(url.protocol) && !url.username && !url.password && !url.pathname.startsWith("/__reasonix_workspace_media/"); }
  catch { return false; }
}
export class BrowserRecoveryStore {
  private document: Record<string, unknown> = { version: 1, tabs: [] };
  private writable = true;
  private lastSaved = "";
  readonly tabs: RecoveredTab[] = [];
  constructor(private readonly path: string, private readonly warn: (message: string) => void) {
    try {
      const data = readFileSync(path, "utf8");
      if (data.length > 256 * 1024) throw new Error("recovery metadata exceeds budget");
      const document = JSON.parse(data) as Record<string, unknown>;
      if (document.version !== 1 || !Array.isArray(document.tabs)) { this.writable = false; return; }
      this.document = document;
      const ids = new Set<string>();
      for (const candidate of document.tabs.slice(0, 32)) {
        const tab = candidate as RecoveredTab;
        if (!tab || !/^tab-\d+$/.test(tab.id) || !Number.isSafeInteger(Number(tab.id.slice(4))) || ids.has(tab.id) || typeof tab.taskId !== "string" || typeof tab.sessionId !== "string" || typeof tab.url !== "string" || (!recoverableURL(tab.url) && !validFileReference(tab.fileReference))) continue;
        let viewport: BrowserViewport | null = null;
        try { viewport = tab.viewport ? validateViewport(tab.viewport) : null; } catch { continue; }
        ids.add(tab.id);
        this.tabs.push({ ...tab, title: typeof tab.title === "string" ? tab.title.slice(0, 512) : "", viewport });
      }
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") { this.writable = false; warn("Browser tab recovery metadata is unreadable; preserving the original file"); }
    }
  }
  save(tabs: RecoveredTab[]): void {
    if (!this.writable) return;
    const previous = new Map(this.tabs.map(tab => [tab.id, tab]));
    const rows = tabs.filter(tab => recoverableURL(tab.url) || validFileReference(tab.fileReference)).slice(0, 32).map(tab => {
      const row: RecoveredTab = { ...previous.get(tab.id), ...tab, title: tab.title.slice(0, 512) };
      if (row.fileReference) row.url = "";
      for (const key of prohibited) delete row[key];
      return row;
    });
    const temporary = `${this.path}.tmp`;
    try {
      const serialized = JSON.stringify({ ...this.document, version: 1, tabs: rows });
      if (serialized === this.lastSaved) return;
      if (Buffer.byteLength(serialized) > 256 * 1024) throw new Error("recovery metadata exceeds budget");
      mkdirSync(dirname(this.path), { recursive: true });
      writeFileSync(temporary, serialized, { mode: 0o600 });
      renameSync(temporary, this.path);
      this.lastSaved = serialized;
    } catch { rmSync(temporary, { force: true }); this.warn("Browser tab recovery metadata could not be saved"); }
  }
}
