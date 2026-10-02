import { BrowserWindow, MessageChannelMain, session, type WebFrameMain } from "electron";
import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile, appendFile, rename, rm, stat } from "node:fs/promises";
import { join, isAbsolute } from "node:path";
import type { BrowserTab, BrowserSurfaceManager } from "./surfaceManager.js";
import type { GrantRegistry } from "./grants.js";
import { browserFailure } from "./errors.js";
import { abortable } from "./captureQueue.js";
import { scriptCall } from "./pageScripts.js";

interface Recording { id: string; tabId: string; grantId: string; state: "preparing" | "recording" | "finalizing" | "completed" | "cancelled" | "interrupted" | "failed"; bytes: number; width?: number; height?: number; durationMs?: number; path?: string; error?: string }
interface Job { info: Recording; stop(): Promise<Recording>; cancel(reason: string): Promise<void> }
export class BrowserRecorder {
  private job: Job | null = null;
  private readonly history = new Map<string, Recording>();
  constructor(private readonly surfaces: BrowserSurfaceManager, private readonly grants: GrantRegistry, private readonly resources: string) {}
  async close(): Promise<void> { await this.job?.cancel("browser service stopped"); }
  async stopCurrent(): Promise<void> {
    const job = this.job;
    if (!job) return;
    await job.stop();
  }

  async request(tab: BrowserTab, grantId: string, action: string, recordingId: string, directory: string, durationSeconds = 20, signal?: AbortSignal): Promise<Recording> {
    if (action === "start") {
      if (this.job) throw browserFailure("surface_unavailable", "another recording is active");
      if (!isAbsolute(directory) || !(await stat(directory)).isDirectory()) throw new Error("recording requires an existing task directory");
      if (!Number.isInteger(durationSeconds) || durationSeconds < 1 || durationSeconds > 90) throw new Error("recording duration must be 1–90 seconds");
      signal?.throwIfAborted();
      return this.start(tab, grantId, directory, durationSeconds, signal);
    }
    const info = this.history.get(recordingId);
    if (!info || info.grantId !== grantId || info.tabId !== tab.id) throw new Error("recording does not belong to this task and tab");
    if (action === "status") return { ...info };
    if (action === "stop") return this.job?.info.id === recordingId ? this.job.stop() : { ...info };
    if (action === "cancel") { if (this.job?.info.id === recordingId) await this.job.cancel("cancelled by user"); return { ...info }; }
    throw new Error("invalid recording action");
  }

  async userRequest(tab: BrowserTab, action: string, directory: string): Promise<Omit<Recording, "grantId"> | null> {
    const current = this.job?.info.tabId === tab.id ? this.job.info : [...this.history.values()].reverse().find(info => info.tabId === tab.id);
    let result: Recording | undefined;
    if (action === "start") result = await this.start(tab, "user", directory, 20, undefined, true);
    else if (action === "status") result = current;
    else if (action === "stop" && this.job?.info.tabId === tab.id) result = await this.job.stop();
    else if (action === "cancel" && this.job?.info.tabId === tab.id) { await this.job.cancel("cancelled by user"); result = current; }
    if (!result) return null;
    const { grantId: _grant, ...publicResult } = result;
    return publicResult;
  }

  private async start(tab: BrowserTab, grantId: string, directory: string, seconds: number, signal?: AbortSignal, userInitiated = false): Promise<Recording> {
    if (this.job) throw browserFailure("surface_unavailable", "another recording is active");
    const id = randomUUID();
    const info: Recording = { id, tabId: tab.id, grantId, state: "preparing", bytes: 0 };
    const controller = new AbortController();
    const epoch = tab.epoch, lifecycleEpoch = tab.lifecycleEpoch, revision = tab.viewportRevision;
    let win: BrowserWindow | undefined;
    let release: (() => void) | undefined;
    let finished = false;
    let fileWrites = Promise.resolve();
    let finishResolve!: (value: Recording) => void;
    const completion = new Promise<Recording>(resolve => { finishResolve = resolve; });
    const temporary = join(directory, `recording-${id}.webm.part`), target = join(directory, `recording-${id}.webm`), html = join(directory, `recorder-${id}.html`);
    const captureSession = session.fromPartition(`reasonix-recorder-${id}`);
    const { port1, port2 } = new MessageChannelMain();
    let stopTimer: ReturnType<typeof setTimeout> | undefined;
    let finalizationTimer: ReturnType<typeof setTimeout> | undefined;
    let initialElapsed = 0;
    let captureGeometry: { width: number; height: number; scale: number } | undefined;
    const offSurfaces = this.surfaces.subscribe(() => {
      const size = tab.view.captureSurfaceSize?.();
      const changedSurface = captureGeometry && size && (size.width !== captureGeometry.width || size.height !== captureGeometry.height || (tab.view.inputScale?.() ?? 1) !== captureGeometry.scale);
      const controlChanged = !userInitiated && (tab.mode !== "agent" || tab.epoch !== epoch);
      if (this.surfaces.get(tab.id) !== tab || controlChanged || tab.view.page.isDestroyed() || tab.lifecycleEpoch !== lifecycleEpoch || tab.viewportRevision !== revision || changedSurface) void cancel("target closed, changed, crashed or taken over");
    });
    const offGrant = this.grants.onRevoke(grant => { if (!userInitiated && grant.grantId === grantId) void cancel("grant revoked"); });
    const cleanup = async (discard: boolean, outcome: Partial<Recording>) => {
      if (finished) return;
      finished = true;
      // Terminal status promises the global slot and all temporary resources
      // are released. Keep cleanup visible as finalizing until that is true.
      info.state = "finalizing";
      clearTimeout(stopTimer); clearTimeout(finalizationTimer);
      offSurfaces(); offGrant(); controller.abort();
      captureSession.setDisplayMediaRequestHandler(null);
      port1.close();
      if (win && !win.isDestroyed()) win.destroy();
      release?.();
      await fileWrites.catch(() => {});
      try {
        await rm(html, { force: true });
        if (discard) { await rm(temporary, { force: true }); await rm(target, { force: true }); }
      } catch { outcome = { state: "interrupted", error: "recording cleanup failed", path: undefined }; }
      finally {
        if (this.job?.info.id === id) this.job = null;
        Object.assign(info, outcome);
        finishResolve({ ...info });
      }
    };
    const cancel = async (reason: string, failure = false) => {
      if (finished) { await completion; return; }
      port1.postMessage({ type: "cancel" });
      await cleanup(true, { state: failure ? "failed" : reason === "cancelled by user" ? "cancelled" : "interrupted", error: reason });
    };
    const stop = async () => {
      if (finished) return completion;
      if (info.state === "preparing") await cancel("cancelled by user");
      if (info.state === "recording") {
        info.state = "finalizing";
        port1.postMessage({ type: "stop" });
        finalizationTimer = setTimeout(() => { void cancel("WebM finalization deadline exceeded"); }, 10_000);
      }
      return completion;
    };
    this.job = { info, stop, cancel };
    const abortStart = () => { void cancel("recording request cancelled"); };
    signal?.addEventListener("abort", abortStart, { once: true });
    this.history.set(id, info);
    while (this.history.size > 10) this.history.delete(this.history.keys().next().value!);
    try {
      release = await tab.view.prepareCapture?.(controller.signal, true);
      if (!userInitiated) this.grants.verifyTab(grantId, tab.taskId, tab.sessionId);
      controller.signal.throwIfAborted();
      const geometry = await abortable(tab.view.page.executeJavaScriptInIsolatedWorld(1, [{ code: scriptCall("pageReady", {}) }]), controller.signal) as { width: number; height: number };
      const surface = tab.view.captureSurfaceSize?.() ?? geometry;
      captureGeometry = { ...surface, scale: tab.view.inputScale?.() ?? 1 };
      const scale = tab.view.inputScale?.() ?? 1;
      if (geometry.width * scale > surface.width + 1 || geometry.height * scale > surface.height + 1) throw browserFailure("surface_unavailable", "recording viewport is clipped; choose Fit and show the page before retrying");
      const cropWidth = Math.min(1, geometry.width * scale / surface.width), cropHeight = Math.min(1, geometry.height * scale / surface.height);
      if (!Number.isInteger(geometry.width) || geometry.width <= 0 || geometry.width > 3840 || !Number.isInteger(geometry.height) || geometry.height <= 0 || geometry.height > 2160) throw new Error("recording viewport exceeds pixel budget");
      const source = (await readFile(join(this.resources, "browser-recorder.js"), "utf8")).replace(/<\/script/gi, "<\\/script");
      controller.signal.throwIfAborted();
      const hash = createHash("sha256").update(source).digest("base64");
      fileWrites = fileWrites.then(() => writeFile(html, `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'sha256-${hash}'; media-src blob:"><script>${source}</script>`, { mode: 0o600 }));
      await fileWrites; controller.signal.throwIfAborted();
      fileWrites = fileWrites.then(() => writeFile(temporary, Buffer.alloc(0), { mode: 0o600, flag: "wx" }));
      await fileWrites; controller.signal.throwIfAborted();
      win = new BrowserWindow({ show: false, focusable: false, skipTaskbar: true, webPreferences: { session: captureSession, sandbox: true, contextIsolation: true, nodeIntegration: false, backgroundThrottling: false, preload: join(this.resources, "recorder-preload.cjs") } });
      const targetFrame = tab.view.page.mainFrame as WebFrameMain;
      captureSession.setDisplayMediaRequestHandler((request, callback) => {
        if (finished || request.frame !== win?.webContents.mainFrame || request.audioRequested || !request.videoRequested || targetFrame.detached || !userInitiated && tab.mode !== "agent") callback({});
        else callback({ video: targetFrame });
      });
      win.webContents.once("render-process-gone", () => { void cancel("recorder renderer crashed"); });
      const started = new Promise<void>((resolve, reject) => {
        port1.on("message", ({ data }) => {
          if (finished) return;
          if (data.type === "ready") port1.postMessage({ type: "start", ...geometry, cropWidth, cropHeight });
          if (data.type === "started") { initialElapsed = Number(data.elapsedMs) || 0; info.state = "recording"; resolve(); }
          if (data.type === "error") { reject(new Error(String(data.message))); void cancel("recorder failed: " + String(data.message), true); }
          if (data.type === "chunk") {
            const chunk = Buffer.from(data.data as ArrayBuffer);
            info.bytes += chunk.length;
            if (info.bytes > 64 * 1024 * 1024) { void cancel("recording exceeded 64 MiB", true); return; }
            fileWrites = fileWrites.then(() => appendFile(temporary, chunk));
            void fileWrites.catch(() => cancel("recording file write failed", true));
          }
          if (data.type === "stopped") {
            void (async () => {
              try {
                await fileWrites;
                if (finished) return;
                const bytes = await readFile(temporary);
                if (bytes.length !== info.bytes || bytes.length < 32 || bytes.readUInt32BE(0) !== 0x1a45dfa3 || data.width !== geometry.width || data.height !== geometry.height || !Number.isFinite(data.durationMs) || data.durationMs <= 0) throw new Error("invalid WebM result");
                await rename(temporary, target);
                if (finished) { await rm(target, { force: true }); return; }
                await cleanup(false, { state: "completed", width: data.width, height: data.height, durationMs: data.durationMs, path: target });
              } catch { await cancel("WebM validation failed", true); }
            })();
          }
        });
      });
      port1.start();
      await win.loadFile(html);
      controller.signal.throwIfAborted();
      win.webContents.postMessage("reasonix-recorder-port", null, [port2]);
      const timer = setTimeout(() => controller.abort(), 10_000);
      try { await abortable(started, controller.signal); } finally { clearTimeout(timer); }
      stopTimer = setTimeout(() => { void stop(); }, Math.max(0, seconds * 1000 - initialElapsed));
      return { ...info };
    } catch (error) { await cancel(String(error), true); throw error; }
    finally { signal?.removeEventListener("abort", abortStart); }
  }
}
