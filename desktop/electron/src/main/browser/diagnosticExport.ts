import type { BrowserSurfaceManager, BrowserTab } from "./surfaceManager.js";
import type { BrowserGrant, GrantRegistry } from "./grants.js";
import type { BrowserRequestTrace } from "./diagnosticContext.js";
import { diagnosticText, diagnosticURL, type DiagnosticBuffer, type PageDiagnostic } from "./diagnostics.js";

interface Entry { scope: string; sequence: number; time: number; kind: string; [key: string]: unknown }
interface Tracked { scope: string; state: string; tab: BrowserTab; buffer?: DiagnosticBuffer; off?: () => void }
const LIMIT = 512, BYTE_LIMIT = 256 * 1024;

// One bounded application-wide window, independent of tab/grant lifetime.
// Attribution is supplied by the trusted Go host, never by page content.
export class BrowserDiagnosticExport {
  private entries: Entry[] = [];
  private bytes = 0;
  private sequence = 0;
  private dropped = 0;
  private readonly startedAt = Date.now();
  private readonly tracked = new Map<string, Tracked>();
  private readonly stop: (() => void)[];
  constructor(private surfaces: BrowserSurfaceManager, private grants: GrantRegistry, private metadata: { build: string; version: string; platform: string }) {
    this.stop = [surfaces.subscribe(() => this.sync()), grants.onRevoke(grant => {
      this.add(grant.diagnosticScope ?? "", "lifecycle", { event: "grant_revoked", taskId: grant.taskId });
    })];
  }
  bind(grant: BrowserGrant): void {
    if (!grant.diagnosticScope || !/^[a-f0-9]{64}$/.test(grant.diagnosticScope)) return;
    this.add(grant.diagnosticScope, "lifecycle", { event: "grant_installed", taskId: grant.taskId, runtimeGeneration: grant.generation });
    this.sync();
  }
  trace(scope: string, event: BrowserRequestTrace): void {
    const identifier = (value?: string) => value && /^[\w./-]{1,160}$/.test(value) ? value : undefined;
    this.add(scope, "host", { requestId: identifier(event.requestId), operationId: identifier(event.operationId), tabId: identifier(event.tabId), method: identifier(event.method), phase: identifier(event.phase), elapsedMs: event.elapsedMs, errorKind: identifier(event.errorKind), command: identifier(event.command), target: identifier(event.target) });
  }
  private add(scope: string, kind: string, fields: Record<string, unknown>): void {
    if (!scope) return;
    const item: Entry = { ...fields, scope, kind, sequence: ++this.sequence, time: Date.now() };
    this.entries.push(item);
    this.bytes += Buffer.byteLength(JSON.stringify(item));
    while (this.entries.length > LIMIT || this.bytes > BYTE_LIMIT) {
      this.bytes -= Buffer.byteLength(JSON.stringify(this.entries.shift()!));
      this.dropped++;
    }
  }
  private page(scope: string, tabId: string, entry: PageDiagnostic): void {
    this.add(scope, "page", { tabId, pageSequence: entry.sequence, pageTime: entry.time, category: entry.kind, message: diagnosticText(entry.message), url: entry.url ? diagnosticURL(entry.url) : undefined, status: entry.status });
  }
  private sync(): void {
    const alive = new Set<string>();
    for (const tab of this.surfaces.all()) {
      alive.add(tab.id);
      let record = this.tracked.get(tab.id);
      const scope = record?.scope ?? this.grants.diagnosticScopeForTab(tab.taskId, tab.sessionId);
      if (!scope) continue;
      if (!record) { record = { scope, state: "", tab }; this.tracked.set(tab.id, record); }
      const state = { tabId: tab.id, taskId: tab.taskId, documentEpoch: tab.epoch, lifecycleEpoch: tab.lifecycleEpoch, viewportRevision: tab.viewportRevision, viewport: tab.viewport, mode: tab.mode, loading: tab.loading, crashes: tab.crashes, errorCode: tab.error?.code, active: this.surfaces.activeTabId === tab.id, webContentsId: tab.view.page.id };
      const serialized = JSON.stringify(state);
      if (serialized !== record.state) {
        this.add(scope, "lifecycle", { event: record.state ? "tab_changed" : "tab_opened", ...state });
        record.state = serialized;
      }
      const buffer = tab.view.diagnostics;
      if (buffer !== record.buffer) {
        record.off?.(); record.buffer = buffer;
        if (buffer) {
          const snapshot = buffer.read();
          this.add(scope, "lifecycle", { event: "page_diagnostics_attached", tabId: tab.id, truncated: snapshot.truncated });
          for (const item of snapshot.entries) this.page(scope, tab.id, item);
          record.off = buffer.subscribe(item => this.page(scope, tab.id, item));
        }
      }
    }
    for (const [id, record] of this.tracked) {
      if (!alive.has(id)) {
        record.off?.(); this.tracked.delete(id);
        this.add(record.scope, "lifecycle", { event: "tab_closed", tabId: id });
      }
    }
  }
  read(scope: string) {
    if (!/^[a-f0-9]{64}$/.test(scope)) throw new Error("invalid diagnostic scope");
    this.sync();
    const entries = this.entries.filter(entry => entry.scope === scope).map(({ scope: _scope, ...entry }) => ({ ...entry }));
    return {
      available: true, scope, schemaVersion: 1, ...this.metadata, startedAt: this.startedAt, capturedAt: Date.now(),
      sessionEvidenceAvailable: entries.length > 0,
      retention: "In-memory application window; cleared on shell restart. Truncation may be caused by other sessions. Events without explicit attribution are omitted.",
      truncated: this.dropped > 0, limit: LIMIT, byteLimit: BYTE_LIMIT,
      tabs: [...this.tracked.values()].filter(row => row.scope === scope).map(row => ({ tabId: row.tab.id, pageDiagnosticsAvailable: Boolean(row.buffer) })),
      entries,
    };
  }
  dispose(): void {
    for (const off of this.stop) off();
    for (const row of this.tracked.values()) row.off?.();
    this.tracked.clear(); this.entries = []; this.bytes = 0;
  }
}
