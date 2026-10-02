import { WebContentsView, session as electronSession, BrowserWindow, screen, type Session, type WebContents, type WebPreferences, type MouseInputEvent } from "electron";
import { disposeDebugger } from "./debuggerLease.js";
import { browserFailure } from "./errors.js";
import { abortable } from "./captureQueue.js";
import { DiagnosticBuffer } from "./diagnostics.js";
import { viewportScale, type BrowserViewport } from "./viewport.js";
import { dispatchMouseInput } from "./mouseInput.js";
import type { Logger } from "../log.js";
import { isBlockedNavigation, isPopupURL, type GuestView, type GuestViewEvents, type GuestViewFactory } from "./guestView.js";

const GUEST_PERMISSIONS = new Set(["clipboard-sanitized-write", "fullscreen"]);

export interface ElectronGuestViewDeps {
  window(): BrowserWindow | null;
  preloadPath: string;
  log: Logger;
  onSession?(partition: string, session: Session): void;
}

// Website views are untrusted: sandboxed, isolated, no Node, only the guest
// preload that reports user input. Every partition session gets the same
// permission policy the first time it is seen.
export class ElectronGuestViewFactory implements GuestViewFactory {
  private readonly sessions = new Set<string>();
  private readonly diagnosticBuffers = new Map<number, DiagnosticBuffer>();

  constructor(private readonly deps: ElectronGuestViewDeps) {}

  create(partition: string, inherited?: WebPreferences): GuestView {
    const win = this.deps.window();
    if (!win) throw new Error("browser tabs need the main window");
    this.prepareSession(partition);
    const view = new WebContentsView({
      webPreferences: {
        ...inherited,
        partition,
        preload: this.deps.preloadPath,
        sandbox: true,
        contextIsolation: true,
        nodeIntegration: false,
        nodeIntegrationInSubFrames: false,
        nodeIntegrationInWorker: false,
        webviewTag: false,
        spellcheck: true,
        backgroundThrottling: true,
      },
    });
    win.contentView.addChildView(view);
    view.setVisible(false);
    const guest = new ElectronGuestView(view, win, this, this.deps.log);
    this.diagnosticBuffers.set(view.webContents.id, guest.diagnostics);
    const id = view.webContents.id;
    view.webContents.once("destroyed", () => this.diagnosticBuffers.delete(id));
    return guest;
  }

  private prepareSession(partition: string): void {
    if (this.sessions.has(partition)) return;
    this.sessions.add(partition);
    const session = electronSession.fromPartition(partition);
    session.setPermissionRequestHandler((_contents, permission, callback) => callback(GUEST_PERMISSIONS.has(permission)));
    session.setPermissionCheckHandler((_contents, permission) => GUEST_PERMISSIONS.has(permission));
    session.webRequest.onCompleted(details => {
      if (details.statusCode >= 400) this.diagnosticBuffers.get(details.webContentsId ?? -1)?.add({ kind: "network", message: `HTTP ${details.statusCode}`, status: details.statusCode, url: details.url });
    });
    session.webRequest.onErrorOccurred(details => {
      this.diagnosticBuffers.get(details.webContentsId ?? -1)?.add({ kind: "network", message: details.error, url: details.url });
    });
    this.deps.onSession?.(partition, session);
  }
}

class ElectronGuestView implements GuestView {
  readonly diagnostics = new DiagnosticBuffer();
  private destroyed = false;
  private desiredBounds: Electron.Rectangle = { x: 0, y: 0, width: 1280, height: 720 };
  private desiredVisible = false;
  private releaseCapture: (() => void) | null = null;
  private observations = 0;
  private viewport: BrowserViewport | null = null;
  private emulated = false;
  private captureHost: BrowserWindow | null = null;

  constructor(
    private readonly view: WebContentsView,
    private readonly win: BrowserWindow,
    private readonly factory: GuestViewFactory,
    private readonly log: Logger,
  ) {
    const wc = view.webContents;
    const debuggerAPI = wc.debugger;
    wc.on("render-process-gone", () => disposeDebugger(debuggerAPI));
    wc.once("destroyed", () => disposeDebugger(debuggerAPI, false));
    debuggerAPI.on("detach", () => disposeDebugger(debuggerAPI, false));
  }

  get page(): WebContents {
    return this.view.webContents;
  }

  bind(events: GuestViewEvents): void {
    const wc = this.view.webContents;
    wc.on("dom-ready", () => this.applyViewport());
    wc.on("console-message", details => {
      if (details.level === "warning" || details.level === "error") this.diagnostics.add({ kind: /^Uncaught\b/.test(details.message) ? "exception" : "console", message: details.message, url: details.sourceId });
    });
    wc.on("did-start-loading", () => events.onStartLoading());
    wc.on("did-stop-loading", () => events.onStopLoading());
    wc.on("did-navigate", (_event, url) => events.onNavigate(url, false));
    wc.on("did-navigate-in-page", (_event, url, isMainFrame) => {
      if (isMainFrame) events.onNavigate(url, true);
    });
    wc.on("page-title-updated", (_event, title) => events.onTitle(title));
    wc.on("did-fail-load", (_event, code, description, url, isMainFrame) => {
      if (code !== -3) this.diagnostics.add({ kind: "navigation", message: description, url });
      if (isMainFrame && code !== -3) events.onFailLoad(code, description, url);
    });
    wc.on("render-process-gone", (_event, details) => events.onRenderProcessGone(details.reason));
    wc.on("destroyed", () => events.onDestroyed());
    wc.on("will-navigate", (details) => this.guardNavigation(details, details.url));
    wc.on("will-frame-navigate", (details) => this.guardNavigation(details, details.url));
    wc.on("will-redirect", (details) => this.guardNavigation(details, details.url));
    wc.on("will-attach-webview", (event) => event.preventDefault());
    wc.setWindowOpenHandler(({ url, disposition }) => {
      if (!isPopupURL(url)) return { action: "deny" };
      const adopt = events.onPopup(url, disposition);
      if (!adopt) return { action: "deny" };
      return {
        action: "allow",
        createWindow: (options) => {
          const child = this.factory.create(String(options.webPreferences?.partition ?? ""), options.webPreferences);
          adopt(child);
          return child.page as WebContents;
        },
      };
    });
  }

  private guardNavigation(details: { preventDefault(): void }, url: string): void {
    if (!isBlockedNavigation(url)) return;
    details.preventDefault();
    this.log.warn(`blocked browser navigation to ${url.slice(0, 120)}`);
  }

  setBounds(bounds: Electron.Rectangle): void {
    this.desiredBounds = bounds;
    if (!this.destroyed && !this.releaseCapture) this.view.setBounds(bounds);
    if (!this.releaseCapture) this.applyViewport();
  }

  inputScale(): number { return (this.page.getZoomFactor() || 1) * (this.captureHost && this.viewport ? 1 : viewportScale(this.viewport, this.view.getBounds())); }
  capturePixelRatio(): number { return screen.getDisplayMatching((this.captureHost ?? this.win).getBounds()).scaleFactor; }

  setViewport(viewport: BrowserViewport | null): void {
    this.viewport = viewport;
    this.applyViewport();
  }

  private applyViewport(): void {
    if (this.destroyed || this.page.isDestroyed()) return;
    if (!this.page.getURL()) return;
    if (!this.viewport) {
      if (this.emulated) this.page.disableDeviceEmulation();
      this.emulated = false;
      return;
    }
    const { width, height } = this.viewport;
    this.page.setZoomFactor(1);
    this.page.enableDeviceEmulation({ screenPosition: "desktop", screenSize: { width, height }, deviceScaleFactor: 1, viewSize: { width, height }, viewPosition: { x: 0, y: 0 }, scale: this.captureHost ? 1 : viewportScale(this.viewport, this.view.getBounds()) });
    this.emulated = true;
  }

  setVisible(visible: boolean): void {
    this.desiredVisible = visible;
    if (!this.destroyed && !this.releaseCapture) this.view.setVisible(visible);
  }

  presentForUser(): void { this.releaseCapture?.(); }
  captureSurfaceSize(): { width: number; height: number } { return this.view.getBounds(); }
  async sendMouseInput(event: MouseInputEvent, verify?: () => void): Promise<void> {
    await dispatchMouseInput(this.page, event, () => this.inputScale(), verify);
  }
  prepareObservation(): () => void {
    this.observations++;
    this.page.setBackgroundThrottling(false);
    let released = false;
    return () => { if (released) return; released = true; this.observations--; if (!this.page.isDestroyed() && !this.releaseCapture && this.observations === 0) this.page.setBackgroundThrottling(true); };
  }

  async prepareCapture(signal: AbortSignal, recording = false): Promise<() => void> {
    signal.throwIfAborted();
    if (this.destroyed || this.page.isDestroyed()) throw browserFailure("surface_unavailable", "page was closed");
    if ((!recording || process.platform !== "darwin") && this.desiredVisible && this.win.isVisible() && !this.win.isMinimized() && this.desiredBounds.width > 0 && this.desiredBounds.height > 0) return () => {};
    // Background capture is qualified independently for each native platform.
    if (process.platform !== "darwin") throw browserFailure("capability_unsupported", "background capture is not qualified on this platform; show the page and retry");
    if (this.releaseCapture) return () => {}; // The recording owns the existing host.
    const width = this.viewport?.width ?? (this.desiredBounds.width > 0 ? this.desiredBounds.width : 1280);
    const height = this.viewport?.height ?? (this.desiredBounds.height > 0 ? this.desiredBounds.height : 720);
    const host = new BrowserWindow({ width, height, useContentSize: true, show: false, frame: false, focusable: false, skipTaskbar: true, opacity: 0, webPreferences: { sandbox: true, contextIsolation: true, nodeIntegration: false } });
    this.captureHost = host;
    let released = false;
    const release = () => {
      if (released) return;
      released = true;
      signal.removeEventListener("abort", release);
      if (!host.isDestroyed()) host.contentView.removeChildView(this.view);
      this.releaseCapture = null;
      this.captureHost = null;
      if (!this.destroyed && !this.page.isDestroyed()) {
        this.page.setBackgroundThrottling(this.observations === 0);
        if (!this.win.isDestroyed()) {
          this.win.contentView.addChildView(this.view);
          this.view.setBounds(this.desiredBounds);
          this.view.setVisible(this.desiredVisible);
          this.applyViewport();
        }
      }
      if (!host.isDestroyed()) host.destroy();
    };
    this.releaseCapture = release;
    signal.addEventListener("abort", release, { once: true });
    const preparation = new AbortController();
    const preparationSignal = AbortSignal.any([signal, preparation.signal]);
    const preparationTimer = setTimeout(() => preparation.abort(browserFailure("surface_unavailable", "capture surface preparation exceeded 5000ms")), 5000);
    try {
      this.win.contentView.removeChildView(this.view);
      host.contentView.addChildView(this.view);
      this.view.setBounds({ x: 0, y: 0, width, height });
      this.applyViewport();
      this.view.setVisible(true);
      this.page.setBackgroundThrottling(false);
      host.setIgnoreMouseEvents(true);
      host.showInactive();
      const deadline = Date.now() + 5000;
      // Probe the compositor, rather than treating showInactive or elapsed time
      // as readiness. A bounded cadence only schedules the next actual probe.
      while (true) {
        signal.throwIfAborted();
        try {
          const probe = await abortable(this.page.capturePage({ x: 0, y: 0, width: 1, height: 1 }, { stayHidden: true, stayAwake: true }), preparationSignal);
          if (!probe.isEmpty()) return release;
        } catch (error) { if (preparationSignal.aborted) throw error; }
        if (Date.now() >= deadline) throw browserFailure("surface_unavailable", "capture surface preparation exceeded 5000ms");
        await abortable(new Promise<void>((resolve) => setTimeout(resolve, 16)), signal);
      }
    } catch (error) { release(); throw error; }
    finally { clearTimeout(preparationTimer); }
  }

  // Detach first so the window never paints a closing view, then close the
  // WebContents; the prototype's quit order that never left orphans.
  destroy(): void {
    if (this.destroyed) return;
    this.destroyed = true;
    this.releaseCapture?.();
    if (!this.win.isDestroyed()) {
      try {
        this.win.contentView.removeChildView(this.view);
      } catch (error) {
        this.log.warn(`removeChildView failed: ${String(error)}`);
      }
    }
    const wc = this.view.webContents;
    if (!wc.isDestroyed()) { disposeDebugger(wc.debugger); wc.close(); }
  }
}
