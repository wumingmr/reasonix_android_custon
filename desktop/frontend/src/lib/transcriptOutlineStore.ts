import { app } from "./bridge";
import { readNativeTranscriptOutline } from "./nativeTranscriptHistory";
import { readBoundHistoryOutline } from "./historyReadBinding";
import { observeOutline } from "./transcriptOutlineSignals";
import type { HistoryOutlineEntry, HistoryOutlinePage, HistoryOutlineRequest } from "../generated/desktopContract.generated";

export type OutlineRead = (tabId: string, request: HistoryOutlineRequest) => Promise<HistoryOutlinePage>;
export type TranscriptOutlineView = {
  mode: "loading" | "ready" | "error" | "unsupported";
  generation: string; snapshotSequence?: number; totalTurns: number;
  entries: ReadonlyMap<number, HistoryOutlineEntry>; errors: ReadonlyMap<number, string>;
};
const EMPTY: TranscriptOutlineView = Object.freeze({ mode: "loading", generation: "", totalTurns: 0, entries: new Map(), errors: new Map() });
export const OUTLINE_PAGE_SIZE = 128;
const MAX_PAGES = 6;
const MAX_BYTES = 8 << 20;
type Page = { entries: HistoryOutlineEntry[]; bytes: number; touched: number };
type Binding = {
  identity: string; read: OutlineRead; active: number; epoch: number; dirty: boolean;
  view: TranscriptOutlineView; pages: Map<number, Page>; pending: Map<number, Promise<void>>;
  timer?: ReturnType<typeof setTimeout>; lastRead: number;
  refresh?: Promise<void>;
  releaseSignal?: () => void;
  range?: [number, number];
};

/** Sparse, fixed-cut directory. Body residency never determines the rail size. */
export class TranscriptOutlineStore {
  private bindings = new Map<string, Binding>();
  private listeners = new Map<string, Set<() => void>>();
  private clock = 0;
  constructor(private now = () => Date.now()) {}
  getView(tab: string): TranscriptOutlineView { return this.bindings.get(tab)?.view ?? EMPTY; }
  subscribe(tab: string, listener: () => void): () => void {
    const set = this.listeners.get(tab) ?? new Set();
    this.listeners.set(tab, set); set.add(listener);
    return () => { set.delete(listener); if (!set.size) this.listeners.delete(tab); };
  }
  private publish(tab: string, b: Binding): void {
    const entries = new Map<number, HistoryOutlineEntry>();
    for (const page of b.pages.values()) for (const entry of page.entries) entries.set(entry.turn, entry);
    b.view = { ...b.view, entries };
    for (const listener of [...(this.listeners.get(tab) ?? [])]) listener();
  }
  activate(tab: string, identity: string, read: OutlineRead, knownTurns: number): () => void {
    let b = this.bindings.get(tab);
    if (!b || b.identity !== identity) {
      this.release(tab);
      b = { identity, read, active: 0, epoch: 0, dirty: true, view: { ...EMPTY, totalTurns: knownTurns }, pages: new Map(), pending: new Map(), lastRead: -Infinity };
      this.bindings.set(tab, b);
      b.releaseSignal = observeOutline(tab, signal => signal === "suspend" ? this.suspend(tab) : this.dirty(tab));
    }
    b.active++; b.read = read;
    if (b.dirty) this.schedule(tab, b);
    const owner = b;
    return () => {
      if (this.bindings.get(tab) !== owner) return;
      owner.active = Math.max(0, owner.active - 1);
      if (!owner.active) this.release(tab);
    };
  }
  release(tab: string): void {
    const b = this.bindings.get(tab);
    if (b) { b.epoch++; clearTimeout(b.timer); b.releaseSignal?.(); }
    this.bindings.delete(tab);
  }
  suspend(tab: string): void {
    const b = this.bindings.get(tab);
    if (!b) return;
    b.epoch++; b.pending.clear(); b.refresh = undefined; clearTimeout(b.timer); b.timer = undefined; b.dirty = true;
  }
  dirty(tab: string): void {
    const b = this.bindings.get(tab);
    if (!b) return;
    b.dirty = true;
    if (b.active) this.schedule(tab, b);
  }
  private schedule(tab: string, b: Binding): void {
    if (b.timer !== undefined || b.refresh) return;
    b.timer = setTimeout(() => {
      b.timer = undefined;
      if (b.active && this.bindings.get(tab) === b) void this.refresh(tab);
    }, Math.max(0, 250 - (this.now() - b.lastRead)));
  }
  async refresh(tab: string): Promise<void> {
    const b = this.bindings.get(tab);
    if (!b) return;
    if (b.refresh) return b.refresh;
    clearTimeout(b.timer); b.timer = undefined;
    b.epoch++; b.pending.clear(); b.dirty = false; b.lastRead = this.now();
    // Keep the previous directory visible until the new cut has arrived.
    const run = this.readPage(tab, b, 1, true).finally(() => {
      if (this.bindings.get(tab) !== b || b.refresh !== run) return;
      b.refresh = undefined;
      if (b.dirty && b.active) this.schedule(tab, b);
    });
    b.refresh = run;
    await run;
  }
  async retry(tab: string): Promise<void> {
    const b = this.bindings.get(tab);
    if (!b) return;
    const failed = [...b.view.errors.keys()];
    await this.refresh(tab);
    if (this.bindings.get(tab) !== b || b.view.snapshotSequence === undefined || b.view.mode === "unsupported") return;
    await Promise.all(failed.filter(start => start > 1 && start <= b.view.totalTurns).map(start => this.readPage(tab, b, start)));
    if (b.range) await this.ensure(tab, ...b.range);
  }
  async ensure(tab: string, first: number, last = first): Promise<void> {
    const b = this.bindings.get(tab);
    if (!b || b.view.mode === "unsupported") return;
    b.range = [first, last];
    await b.refresh;
    if (this.bindings.get(tab) !== b) return;
    if (b.view.snapshotSequence === undefined) {
      await (b.pending.get(1) ?? this.readPage(tab, b, 1, true));
      if (b.view.snapshotSequence === undefined) return;
    }
    const starts = new Set<number>();
    for (let turn = Math.max(1, first); turn <= Math.min(last, b.view.totalTurns);) {
      const start = Math.floor((turn - 1) / OUTLINE_PAGE_SIZE) * OUTLINE_PAGE_SIZE + 1;
      starts.add(start); turn = start + OUTLINE_PAGE_SIZE;
    }
    await Promise.all([...starts].map(start => {
      const page = b.pages.get(start);
      if (page) { page.touched = ++this.clock; return; }
      return this.readPage(tab, b, start);
    }));
  }
  async entry(tab: string, turn: number): Promise<HistoryOutlineEntry | undefined> {
    await this.ensure(tab, turn); return this.getView(tab).entries.get(turn);
  }
  private readPage(tab: string, b: Binding, start: number, fresh = false): Promise<void> {
    const pending = b.pending.get(start);
    if (pending) return pending;
    const epoch = b.epoch;
    const current = () => this.bindings.get(tab) === b && b.epoch === epoch;
    const request: HistoryOutlineRequest = { startTurn: start, limit: OUTLINE_PAGE_SIZE,
      ...(fresh ? {} : { generation: b.view.generation, snapshotSequence: b.view.snapshotSequence }) };
    const run = Promise.resolve().then(async () => {
      try {
        const page = await b.read(tab, request);
        if (!current()) return;
        if (page.status === "preparing") { b.dirty = true; this.schedule(tab, b); return; }
        if (page.status === "unsupported") { b.pages.clear(); b.view = { ...EMPTY, mode: "unsupported" }; this.publish(tab, b); return; }
        if (page.status !== "ready") throw new Error(page.status);
        if (!Number.isSafeInteger(page.totalTurns) || page.totalTurns < 0 || !Array.isArray(page.entries)
          || (!fresh && (page.generation !== b.view.generation || page.snapshotSequence !== b.view.snapshotSequence))) throw new Error("invalid outline cut");
        const changedCut = fresh && (page.generation !== b.view.generation || page.snapshotSequence !== b.view.snapshotSequence);
        if (changedCut) b.pages.clear();
        const entries = page.entries.filter(entry => Number.isSafeInteger(entry.turn) && entry.turn >= start && entry.turn < start + OUTLINE_PAGE_SIZE && Boolean(entry.messageId));
        b.pages.set(start, { entries, bytes: JSON.stringify(entries).length * 2, touched: ++this.clock });
        const errors = new Map(changedCut ? [] : b.view.errors); errors.delete(start);
        b.view = { ...b.view, mode: errors.size ? "error" : "ready", generation: page.generation, snapshotSequence: page.snapshotSequence, totalTurns: page.totalTurns, errors };
        this.trim(); this.publish(tab, b);
        if (changedCut && b.range && b.range[0] > OUTLINE_PAGE_SIZE) void this.ensure(tab, ...b.range);
      } catch (error) {
        if (!current()) return;
        const errors = new Map(b.view.errors); errors.set(start, String(error));
        b.view = { ...b.view, mode: "error", errors }; this.publish(tab, b);
      } finally { if (current() && b.pending.get(start) === run) b.pending.delete(start); }
    });
    b.pending.set(start, run); return run;
  }
  private trim(): void {
    let bytes = 0;
    const pages: { tab: string; b: Binding; start: number; page: Page }[] = [];
    for (const [tab, b] of this.bindings) for (const [start, page] of b.pages) { bytes += page.bytes; pages.push({ tab, b, start, page }); }
    pages.sort((a, b) => a.page.touched - b.page.touched);
    for (const item of pages) {
      if (item.b.pages.size <= MAX_PAGES && bytes <= MAX_BYTES) continue;
      item.b.pages.delete(item.start); bytes -= item.page.bytes; this.publish(item.tab, item.b);
    }
  }
}
let singleton: TranscriptOutlineStore | undefined;
export function getTranscriptOutlineStore(): TranscriptOutlineStore { return singleton ??= new TranscriptOutlineStore(); }
const unsupported = (): HistoryOutlinePage => ({ status: "unsupported", entries: [], totalTurns: 0, generation: "", snapshotSequence: 0, coverageSequence: 0, nextTurn: 1, done: true });
export const localOutlineRead: OutlineRead = async (tab, request) => (await readNativeTranscriptOutline(tab, request)) ?? (await readBoundHistoryOutline(tab, request)) ?? (typeof app.SessionHistoryOutlineForTab === "function"
  ? app.SessionHistoryOutlineForTab(tab, request) : unsupported());
export const remoteOutlineRead: OutlineRead = async (tab, request) => (await readNativeTranscriptOutline(tab, request)) ?? (typeof app.RemoteSessionHistoryOutlineForTab === "function"
  ? app.RemoteSessionHistoryOutlineForTab(tab, request) : unsupported());
