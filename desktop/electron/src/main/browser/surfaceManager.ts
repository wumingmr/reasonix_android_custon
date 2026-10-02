import type { Rectangle } from "electron";
import type { BrowserLayoutRect, BrowserNavigateTarget, BrowserTabMode, BrowserTabView, BrowserTakeoverKind } from "../../shared/ipc.js";
import type { Logger } from "../log.js";
import type { GuestView, GuestViewEvents, GuestViewFactory } from "./guestView.js";
import { validateViewport, type BrowserViewport } from "./viewport.js";
import { browserFailure } from "./errors.js";
import { LazyGuestView } from "./lazyGuestView.js";
import { abortable } from "./captureQueue.js";
import type { RecoveredTab } from "./recoveryStore.js";

export const SHARED_PARTITION = "persist:browser";
export const USER_TASK_ID = "user";
export const OPEN_WAIT_MS = 15_000;
// Input the shell dispatches for the agent reaches the page as trusted, so
// the guest preload reports it like a user; reports inside this window after
// a dispatch are the agent's own echo, not a take-over.
export const AGENT_INPUT_GRACE_MS = 750;
export const MAX_CRASH_RELOADS = 3;
export const MIN_ZOOM = 0.25;
export const MAX_ZOOM = 5;

export interface BrowserTab {
  id: string;
  taskId: string;
  sessionId: string;
  view: GuestView;
  partition: string;
  temporary: boolean;
  epoch: number;
  // Page/service invalidation, independent of ordinary human control changes.
  lifecycleEpoch: number;
  viewportRevision: number;
  surfaceRevision: number;
  viewport: BrowserViewport | null;
  operation?: { id: string; phase: string; message?: string };
  fileReference?: import("./recoveryStore.js").FileReference;
  fileReferenceURL?: string;
  mode: BrowserTabMode;
  loading: boolean;
  error: { code: number; description: string } | null;
  zoom: number;
  createdAt: number;
  lastURL: string;
  crashes: number;
  agentInputUntil: number;
}

export interface OpenOptions {
  taskId: string;
  sessionId?: string;
  temporary: boolean;
}

export interface SurfaceManagerDeps {
  views: GuestViewFactory;
  contentSize(): { width: number; height: number } | null;
  onTakeover(tab: BrowserTab, reason: string): void;
  onCrash(tab: BrowserTab, reason: string): void;
  log: Logger;
  now?(): number;
  openWaitMs?: number;
}

export function normaliseBrowserURL(input: string): string {
  const raw = input.trim();
  if (raw === "") throw new Error("empty URL");
  const candidate = /^[a-z][a-z0-9+.-]*:/i.test(raw) ? raw : `https://${raw}`;
  let url: URL;
  try {
    url = new URL(candidate);
  } catch {
    throw new Error(`invalid URL: ${raw.slice(0, 120)}`);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error(`only http(s) URLs can be opened, not ${url.protocol}`);
  return url.href;
}

export function validateLayout(rect: BrowserLayoutRect, content: { width: number; height: number } | null): Rectangle {
  const values = [rect.x, rect.y, rect.width, rect.height];
  if (values.some((value) => typeof value !== "number" || !Number.isFinite(value))) throw new Error("layout rect must be finite numbers");
  const x = Math.max(0, Math.round(rect.x));
  const y = Math.max(0, Math.round(rect.y));
  let width = Math.max(0, Math.round(rect.width));
  let height = Math.max(0, Math.round(rect.height));
  if (content) {
    width = Math.min(width, Math.max(0, content.width - x));
    height = Math.min(height, Math.max(0, content.height - y));
  }
  return { x, y, width, height };
}

export class BrowserSurfaceManager {
  private readonly tabs = new Map<string, BrowserTab>();
  private readonly listeners = new Set<(tabs: BrowserTabView[]) => void>();
  private layout: Rectangle | null = null;
  private overlay = false;
  private activeId: string | null = null;
  private counter = 0;
  private readonly now: () => number;

  constructor(private readonly deps: SurfaceManagerDeps) {
    this.now = deps.now ?? (() => Date.now());
  }

  get activeTabId(): string | null {
    return this.activeId;
  }

  get(tabId: string): BrowserTab | undefined {
    return this.tabs.get(tabId);
  }

  require(tabId: string): BrowserTab {
    const tab = this.tabs.get(tabId);
    if (!tab) throw new Error(`unknown browser tab ${tabId || "(empty)"}`);
    return tab;
  }

  all(): BrowserTab[] {
    return [...this.tabs.values()];
  }

  tabsForTask(taskId: string): BrowserTab[] {
    return this.all().filter((tab) => tab.taskId === taskId);
  }

  tabsForSession(taskId: string, sessionId: string): BrowserTab[] {
    return this.all().filter((tab) => tab.taskId === taskId && tab.sessionId === sessionId);
  }

  list(): BrowserTabView[] {
    return this.all().map((tab) => this.view(tab));
  }

  restore(tabs: RecoveredTab[]): void {
    for (const row of tabs.slice(0, 32)) {
      if (this.tabs.has(row.id) || this.tabs.size >= 32) continue;
      this.counter = Math.max(this.counter, Number(row.id.slice(4)));
      const view = new LazyGuestView(this.deps.views, SHARED_PARTITION, row);
      const tab = this.register(row.id, view, row.taskId, row.sessionId, SHARED_PARTITION, false);
      tab.viewport = row.viewport;
      view.setViewport(row.viewport);
      tab.lastURL = row.url;
      tab.fileReference = row.fileReference;
    }
  }

  recoveryTabs(): RecoveredTab[] {
    return this.all().filter(tab => !tab.temporary).map(tab => ({ id: tab.id, taskId: tab.taskId, sessionId: tab.sessionId, url: tab.view.page.getURL() || tab.lastURL, title: tab.view.page.getTitle(), viewport: tab.viewport, fileReference: tab.fileReference }));
  }

  view(tab: BrowserTab): BrowserTabView {
    const page = tab.view.page;
    const gone = page.isDestroyed();
    return {
      id: tab.id,
      taskId: tab.taskId,
      sessionId: tab.sessionId,
      restorePreview: Boolean(tab.fileReference && tab.view.isPlaceholder?.()),
      url: gone ? tab.lastURL : page.getURL(),
      title: gone ? "" : page.getTitle(),
      loading: tab.loading,
      canGoBack: !gone && page.navigationHistory.canGoBack(),
      canGoForward: !gone && page.navigationHistory.canGoForward(),
      temporary: tab.temporary,
      mode: tab.mode,
      epoch: tab.epoch,
      zoom: tab.zoom,
      viewport: tab.viewport,
      operation: tab.operation,
      active: tab.id === this.activeId,
      error: tab.error,
    };
  }

  subscribe(listener: (tabs: BrowserTabView[]) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  async open(url: string, options: OpenOptions, signal?: AbortSignal): Promise<BrowserTab> {
    signal?.throwIfAborted();
    if (this.tabs.size >= 32) throw new Error("browser tab limit reached (32); close an unused tab first");
    const href = normaliseBrowserURL(url);
    const id = this.nextId();
    const partition = options.temporary ? `temp:${id}` : SHARED_PARTITION;
    const view = this.deps.views.create(partition);
    const tab = this.register(id, view, options.taskId, options.sessionId ?? "", partition, options.temporary);
    if (this.layout) view.setBounds(this.layout);
    // The application renderer owns selection. Agent opens must not replace
    // another task's visible page while its address bar still names that task.
    this.broadcast();
    const load = view.page.loadURL(href).catch((error: unknown) => {
      this.deps.log.warn(`browser tab ${tab.id} load failed: ${String(error)}`);
      tab.loading = false;
      tab.error = { code: 0, description: String(error) };
      this.broadcast();
    });
    let timer: ReturnType<typeof setTimeout> | undefined;
    const ready = Promise.race([load, new Promise<void>((resolve) => { timer = setTimeout(resolve, this.deps.openWaitMs ?? OPEN_WAIT_MS); timer.unref?.(); })]);
    try { await (signal ? abortable(ready, signal) : ready); }
    catch (error) {
      // Open already registered the view. A cancelled waiter has no tab ID
      // to return to the caller, so reclaim only pages still owned by the
      // agent. A user-taken-over page remains theirs.
      if (signal?.aborted && this.tabs.get(tab.id) === tab && tab.mode === "agent") this.close(tab.id);
      throw error;
    }
    finally { clearTimeout(timer); }
    return tab;
  }

  close(tabId: string): void {
    const tab = this.tabs.get(tabId);
    if (!tab) return;
    this.forget(tab);
    tab.view.destroy();
    this.broadcast();
  }

  activate(tabId: string | null): void {
    if (tabId !== null) this.require(tabId);
    if (tabId) this.require(tabId).view.presentForUser?.();
    if (tabId) void this.require(tabId).view.ensureLoaded?.().catch(error => this.deps.log.warn(`Restored browser tab failed to load: ${String(error)}`));
    this.activeId = tabId;
    this.applyVisibility();
    this.broadcast();
  }

  setLayout(rect: BrowserLayoutRect | null): void {
    const next = rect === null ? null : validateLayout(rect, this.deps.contentSize());
    if (next && (next.width !== this.layout?.width || next.height !== this.layout?.height)) {
      for (const tab of this.tabs.values()) {
        tab.surfaceRevision++;
        // Fit changes presentation scale, not the emulated CSS viewport.
        if (!tab.viewport) tab.viewportRevision++;
      }
    }
    this.layout = next;
    this.applyVisibility();
    this.broadcast();
  }

  setOverlay(active: boolean): void {
    if (this.overlay === active) return;
    this.overlay = active;
    this.applyVisibility();
  }

  setViewport(tabId: string, viewport: BrowserViewport | null): void {
    const tab = this.require(tabId);
    if (!tab.view.setViewport) throw browserFailure("capability_unsupported", "viewport emulation is unavailable");
    const next = viewport ? validateViewport(viewport) : null;
    tab.view.setViewport(next);
    tab.viewport = next;
    tab.viewportRevision++;
    tab.epoch++;
    tab.zoom = tab.view.page.getZoomFactor();
    this.broadcast();
  }

  setOperation(tab: BrowserTab, operation: NonNullable<BrowserTab["operation"]>): void {
    if (this.tabs.get(tab.id) !== tab) return;
    tab.operation = operation;
    this.broadcast();
  }

  async navigate(tabId: string, target: BrowserNavigateTarget, signal?: AbortSignal): Promise<BrowserTab> {
    signal?.throwIfAborted();
    const tab = this.require(tabId);
    const page = tab.view.page;
    switch (target.action) {
      case "back":
        if (page.navigationHistory.canGoBack()) page.navigationHistory.goBack();
        return tab;
      case "forward":
        if (page.navigationHistory.canGoForward()) page.navigationHistory.goForward();
        return tab;
      case "reload":
        page.reload();
        return tab;
      case "stop":
        page.stop();
        return tab;
      default:
        break;
    }
    if (typeof target.url !== "string") throw new Error("navigate needs a url or an action");
    const load = page.loadURL(normaliseBrowserURL(target.url)).catch((error: unknown) => {
      this.deps.log.warn(`browser tab ${tab.id} navigation failed: ${String(error)}`);
      tab.loading = false;
      tab.error = { code: 0, description: String(error) };
      this.broadcast();
    });
    await (signal ? abortable(load, signal) : load);
    return tab;
  }

  setZoom(tabId: string, factor: number): void {
    const tab = this.require(tabId);
    if (tab.viewport && tab.view.setViewport) throw new Error("use responsive display scale or exit responsive mode before changing page zoom");
    if (!Number.isFinite(factor)) throw new Error("zoom factor must be a finite number");
    tab.zoom = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, factor));
    tab.view.page.setZoomFactor(tab.zoom);
    this.broadcast();
  }

  toggleDevTools(tabId: string): void {
    const page = this.require(tabId).view.page;
    if (page.isDevToolsOpened()) page.closeDevTools();
    else page.openDevTools({ mode: "detach" });
  }

  resume(tabId: string): void {
    const tab = this.require(tabId);
    if (tab.operation?.phase === "picking") throw new Error("finish element selection or press Esc before returning control");
    if (tab.mode === "agent") return;
    tab.mode = "agent";
    tab.epoch += 1;
    this.broadcast();
  }

  takeover(tabId: string, reason: string): void {
    const tab = this.tabs.get(tabId);
    if (!tab) return;
    tab.epoch += 1;
    tab.mode = "human";
    this.broadcast();
    this.deps.onTakeover(tab, reason);
  }

  // Called with the guest preload's report; the sender id identifies the tab.
  takeoverFromSender(webContentsId: number, kind: BrowserTakeoverKind): boolean {
    const tab = this.all().find((entry) => entry.view.page.id === webContentsId);
    if (!tab) return false;
    if (this.now() < tab.agentInputUntil) return false;
    this.takeover(tab.id, `user ${kind}`);
    return true;
  }

  markAgentInput(tab: BrowserTab): void {
    tab.agentInputUntil = this.now() + AGENT_INPUT_GRACE_MS;
  }

  pauseForRendererLoss(reason: string): void {
    this.layout = null;
    this.activeId = null;
    this.applyVisibility();
    for (const tab of this.all()) {
      tab.lifecycleEpoch += 1;
      this.takeover(tab.id, reason);
    }
  }

  destroyAll(): void {
    const tabs = this.all();
    this.tabs.clear();
    this.activeId = null;
    for (const tab of tabs) {
      tab.mode = "human";
      tab.epoch += 1;
      tab.view.destroy();
    }
    this.broadcast();
  }

  private nextId(): string {
    this.counter += 1;
    return `tab-${this.counter}`;
  }

  private register(id: string, view: GuestView, taskId: string, sessionId: string, partition: string, temporary: boolean): BrowserTab {
    const tab: BrowserTab = {
      id,
      taskId,
      sessionId,
      view,
      partition,
      temporary,
      epoch: 0,
      lifecycleEpoch: 0,
      viewportRevision: 0,
      surfaceRevision: 0,
      viewport: taskId === USER_TASK_ID ? null : { width: 1280, height: 720, scale: "fit" },
      mode: "agent",
      loading: false,
      error: null,
      zoom: 1,
      createdAt: this.now(),
      lastURL: "",
      crashes: 0,
      agentInputUntil: 0,
    };
    this.tabs.set(id, tab);
    view.bind(this.events(tab));
    if (tab.viewport && view.setViewport) view.setViewport(tab.viewport);
    return tab;
  }

  private events(tab: BrowserTab): GuestViewEvents {
    return {
      onStartLoading: () => {
        tab.loading = true;
        this.broadcast();
      },
      onStopLoading: () => {
        tab.loading = false;
        this.broadcast();
      },
      onNavigate: (url, inPage) => {
        if (tab.fileReference && tab.fileReferenceURL !== url) { tab.fileReference = undefined; tab.fileReferenceURL = undefined; }
        tab.epoch += 1;
        tab.lifecycleEpoch += 1;
        tab.lastURL = url;
        if (!inPage) tab.error = null;
        this.broadcast();
      },
      onTitle: () => this.broadcast(),
      onFailLoad: (code, description) => {
        tab.error = { code, description };
        this.broadcast();
      },
      onRenderProcessGone: (reason) => this.recover(tab, reason),
      onDestroyed: () => {
        if (!this.tabs.has(tab.id)) return;
        this.forget(tab);
        this.broadcast();
      },
      onPopup: () => {
        if (!this.tabs.has(tab.id) || this.tabs.size >= 32) return null;
        return (view) => {
          if (!this.tabs.has(tab.id) || this.tabs.size >= 32) { view.destroy(); return; }
          this.register(this.nextId(), view, tab.taskId, tab.sessionId, tab.partition, tab.temporary);
          this.broadcast();
        };
      },
    };
  }

  // A crashed website view never replays anything: it reloads the last
  // committed URL in human mode and the agent must look again.
  private recover(tab: BrowserTab, reason: string): void {
    tab.mode = "human";
    tab.epoch += 1;
    tab.lifecycleEpoch += 1;
    tab.loading = false;
    tab.crashes += 1;
    tab.error = { code: 0, description: `renderer ${reason}` };
    this.broadcast();
    this.deps.onCrash(tab, reason);
    if (tab.crashes > MAX_CRASH_RELOADS || tab.lastURL === "" || tab.view.page.isDestroyed()) return;
    tab.view.page.loadURL(tab.lastURL).catch((error: unknown) => {
      this.deps.log.warn(`browser tab ${tab.id} recovery failed: ${String(error)}`);
    });
  }

  private forget(tab: BrowserTab): void {
    this.tabs.delete(tab.id);
    tab.mode = "human";
    tab.epoch += 1;
    if (this.activeId === tab.id) {
      this.activeId = null;
      this.applyVisibility();
    }
  }

  private applyVisibility(): void {
    for (const tab of this.tabs.values()) {
      const visible = !this.overlay && this.layout !== null && tab.id === this.activeId && this.layout.width > 0 && this.layout.height > 0;
      if (this.layout) tab.view.setBounds(this.layout);
      tab.view.setVisible(visible);
    }
  }

  private broadcast(): void {
    if (this.listeners.size === 0) return;
    const tabs = this.list();
    for (const listener of [...this.listeners]) {
      try {
        listener(tabs);
      } catch (error) {
        this.deps.log.warn(`browser tab listener failed: ${String(error)}`);
      }
    }
  }
}
