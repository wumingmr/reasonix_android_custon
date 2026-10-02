import type { Rectangle, MouseInputEvent } from "electron";
import type { GuestPage, GuestView, GuestViewEvents, GuestViewFactory } from "./guestView.js";
import type { BrowserViewport } from "./viewport.js";
import type { RecoveredTab } from "./recoveryStore.js";
import { browserFailure } from "./errors.js";

// A restored logical tab has no WebContents until explicit UI or granted access.
export class LazyGuestView implements GuestView {
  private live: GuestView | null = null;
  private events?: GuestViewEvents;
  private bounds?: Rectangle;
  private visible = false;
  private destroyed = false;
  private loading: Promise<void> | null = null;
  private viewport: BrowserViewport | null;
  readonly page: GuestPage;
  constructor(private readonly factory: GuestViewFactory, private readonly partition: string, private readonly restored: RecoveredTab) {
    this.viewport = restored.viewport;
    this.page = new Proxy({} as GuestPage, { get: (_target, key) => {
      if (key === "loadURL") return (url: string) => { this.loading = this.materialize().page.loadURL(url); return this.loading; };
      if (this.live) {
        const value = Reflect.get(this.live.page, key);
        return typeof value === "function" ? value.bind(this.live.page) : value;
      }
      if (key === "id") return -1;
      if (key === "getURL") return () => restored.url;
      if (key === "getTitle") return () => restored.title;
      if (key === "isDestroyed") return () => this.destroyed;
      if (key === "isLoading") return () => false;
      if (key === "getZoomFactor") return () => 1;
      if (key === "navigationHistory") return { canGoBack: () => false, canGoForward: () => false, goBack() {}, goForward() {} };
      throw browserFailure("page_not_ready", "restored page has not been loaded");
    } });
  }
  isPlaceholder(): boolean { return this.live === null; }
  private materialize(): GuestView {
    if (this.destroyed) throw browserFailure("stale_document", "restored tab was closed");
    if (!this.live) {
      this.live = this.factory.create(this.partition);
      if (this.events) this.live.bind(this.events);
      if (this.bounds) this.live.setBounds(this.bounds);
      this.live.setViewport?.(this.viewport);
      this.live.setVisible(this.visible);
    }
    return this.live;
  }
  ensureLoaded(): Promise<void> {
    if (this.restored.fileReference && !this.live) return Promise.reject(browserFailure("page_not_ready", "local preview needs fresh file authorization; use Reopen preview"));
    if (this.live) return this.loading ?? Promise.resolve();
    if (!this.loading) this.loading = this.materialize().page.loadURL(this.restored.url);
    return this.loading;
  }
  bind(events: GuestViewEvents): void { this.events = events; this.live?.bind(events); }
  setBounds(bounds: Rectangle): void { this.bounds = bounds; this.live?.setBounds(bounds); }
  setVisible(visible: boolean): void { this.visible = visible; this.live?.setVisible(visible); }
  setViewport(viewport: BrowserViewport | null): void { this.viewport = viewport; this.live?.setViewport?.(viewport); }
  inputScale(): number { return this.live?.inputScale?.() ?? 1; }
  capturePixelRatio(): number { return this.live?.capturePixelRatio?.() ?? 1; }
  presentForUser(): void { this.live?.presentForUser?.(); }
  prepareObservation(): () => void { return this.materialize().prepareObservation?.() ?? (() => {}); }
  async sendMouseInput(event: MouseInputEvent, verify?: () => void): Promise<void> { const view = this.materialize(); if (view.sendMouseInput) await view.sendMouseInput(event, verify); else { verify?.(); view.page.sendInputEvent(event); } }
  captureSurfaceSize(): { width: number; height: number } { return this.live?.captureSurfaceSize?.() ?? this.bounds ?? { width: 1280, height: 720 }; }
  get diagnostics() { return this.live?.diagnostics; }
  async prepareCapture(signal: AbortSignal, recording = false): Promise<() => void> { return await this.materialize().prepareCapture?.(signal, recording) ?? (() => {}); }
  destroy(): void { this.destroyed = true; this.live?.destroy(); }
}
