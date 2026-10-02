import type { BrowserNavigateTarget } from "../../shared/ipc.js";
import type { HostCallTable } from "../hostCalls.js";
import { bool, num, str, strList, type Params } from "../params.js";
import type { ActionExecutor, ActResult } from "./actions.js";
import type { DocumentRegistry } from "./documents.js";
import type { DownloadTracker, HostDownload } from "./downloads.js";
import { browserFailure, takenOver } from "./errors.js";
import { CaptureQueue, abortable } from "./captureQueue.js";
import { queryElements } from "./query.js";
import { scriptCall } from "./pageScripts.js";
import { ISOLATED_WORLD } from "./snapshot.js";
import { randomToken } from "./documents.js";
import { resolveRef } from "./refResolver.js";
import { withFrameOperationSignal } from "./frameRuntime.js";
import { staleReference } from "./errors.js";
import type { BrowserRecorder } from "./recorder.js";
import { rm } from "node:fs/promises";
import { validFileReference } from "./recoveryStore.js";
import { diagnosticText } from "./diagnostics.js";
import type { BrowserDiagnosticExport } from "./diagnosticExport.js";
import { withBrowserDiagnosticRequest } from "./diagnosticContext.js";
import type { GrantRegistry } from "./grants.js";
import { captureScreenshot, type ScreenshotDeps, type ScreenshotResult } from "./screenshot.js";
import { takeSnapshot, type SnapshotResult } from "./snapshot.js";
import type { BrowserSurfaceManager, BrowserTab } from "./surfaceManager.js";

export interface HostBrowserTab {
  id: string;
  url: string;
  title: string;
  loading: boolean;
  temporary: boolean;
  error?: string;
}

export interface BrowserHostDeps {
  surfaces: BrowserSurfaceManager;
  grants: GrantRegistry;
  documents: DocumentRegistry;
  actions: ActionExecutor;
  downloads: DownloadTracker;
  snapshot?(tab: BrowserTab, selector: string): Promise<SnapshotResult>;
  screenshot?(tab: BrowserTab, request: { ref: string; fullPage: boolean; directory: string }): Promise<ScreenshotResult>;
  screenshotDeps?: ScreenshotDeps;
  recorder?: BrowserRecorder;
  captureQueue?: CaptureQueue;
  diagnosticExport?: BrowserDiagnosticExport;
  trace?(event: { requestId: string; phase: string; elapsedMs: number; errorKind?: string }): void;
}

export function hostTab(surfaces: BrowserSurfaceManager, tab: BrowserTab): HostBrowserTab {
  const view = surfaces.view(tab);
  return { id: view.id, url: view.url, title: view.title, loading: view.loading, temporary: view.temporary, error: view.error?.description };
}

// Every call after grant re-verifies the grant and that the browser tab
// belongs to the grant's task; reads and writes both refuse a tab the user
// is operating (-32011).
export function buildBrowserHostCalls(deps: BrowserHostDeps): HostCallTable {
  const { surfaces, grants, documents, downloads } = deps;
  const captureQueue = deps.captureQueue ?? new CaptureQueue();
  const pending = new Map<string, { grantId: string; abort: AbortController }>();
  type Observation = { token: string; epoch: number; revision: number; width: number; height: number; scrollX: number; scrollY: number };
  const observations = new WeakMap<BrowserTab, Observation>();
  const geometry = (tab: BrowserTab) => tab.view.page.executeJavaScriptInIsolatedWorld(ISOLATED_WORLD, [{ code: scriptCall("pageReady", {}) }]) as Promise<{ width: number; height: number; scrollX: number; scrollY: number }>;
  const boundTab = (params: Params): BrowserTab => {
    const tab = surfaces.get(str(params, "tabId"));
    grants.verifyTab(str(params, "grantId"), tab?.taskId, tab?.sessionId);
    return tab as BrowserTab;
  };
  const agentTab = (params: Params): BrowserTab => {
    const tab = boundTab(params);
    if (tab.mode !== "agent") throw takenOver(`tab ${tab.id} is in human mode`);
    return tab;
  };
  const snapshot = deps.snapshot ?? ((tab: BrowserTab, selector: string, verify: () => void) => takeSnapshot(tab.view.page, tab.id, tab.epoch, selector, documents, verify));
  const screenshot = deps.screenshot ?? ((tab: BrowserTab, request: { ref: string; fullPage: boolean; directory: string }, verify: () => void) => {
    const token = documents.currentToken(tab.id);
    const binding = token ? documents.lookup(token) ?? null : null;
    return captureScreenshot(tab.view.page, binding, tab.view.inputScale?.() ?? (tab.view.page.getZoomFactor() || 1), request, { ...deps.screenshotDeps, viewport: tab.viewport, pixelRatio: tab.view.capturePixelRatio?.(), verify });
  });

  const observe = async <T>(params: Params, capture: boolean, work: (tab: BrowserTab, verify: () => void, signal: AbortSignal) => Promise<T>, stableDocument = true, write = false, readPhase = "reading"): Promise<T> => {
    const tab = agentTab(params);
    let epoch = tab.epoch;
    let initializing = tab.view.isPlaceholder?.() ?? false;
    const viewportRevision = tab.viewportRevision;
    const surfaceRevision = tab.surfaceRevision;
    const needsStableSurface = write || readPhase === "capturing";
    const page = tab.view.page;
    const zoom = page.getZoomFactor();
    const grantId = str(params, "grantId");
    const diagnosticScope = grants.verify(grantId).diagnosticScope ?? "";
    const requestId = str(params, "requestId") || randomToken(8);
    const controller = new AbortController();
    const started = performance.now();
    const verify = () => {
      controller.signal.throwIfAborted();
      grants.verifyTab(grantId, tab.taskId, tab.sessionId);
      if (tab.mode !== "agent") throw takenOver(`tab ${tab.id} is in human mode`);
      if (surfaces.get(tab.id) !== tab || (stableDocument && !initializing && tab.epoch !== epoch) || tab.viewportRevision !== viewportRevision || tab.view.page !== page || page.isDestroyed() || page.getZoomFactor() !== zoom) throw browserFailure("stale_document", "page identity or viewport changed during observation");
      if (needsStableSurface && tab.surfaceRevision !== surfaceRevision) throw browserFailure("stale_document", "capture or input surface changed during operation");
    };
    if (requestId && pending.has(requestId)) throw browserFailure("cancelled", "duplicate request identity");
    if (requestId) pending.set(requestId, { grantId, abort: controller });
    const deadline = num(params, "deadline", Date.now() + 9000);
    const timer = setTimeout(() => controller.abort(), Math.max(0, Math.min(deadline - Date.now(), 9000)));
    const unsubscribe = surfaces.subscribe(() => { try { verify(); } catch (error) { controller.abort(error); } });
    const offRevoke = grants.onRevoke(grant => { if (grant.grantId === grantId) controller.abort(); });
    const status = (phase: string, message?: string, errorKind?: string) => {
      surfaces.setOperation(tab, { id: requestId, phase, message });
      // Never log page URLs, text, input, credentials, or exception messages.
      deps.trace?.({ requestId, phase, elapsedMs: Math.round(performance.now() - started), errorKind });
      deps.diagnosticExport?.trace(diagnosticScope, { requestId, operationId: str(params, "operationId"), tabId: tab.id, method: "observe", phase, elapsedMs: Math.round(performance.now() - started), errorKind });
    };
    status("queued");
    try {
      const run = async () => {
        verify();
        status("preparing");
        if (initializing) {
          await abortable(tab.view.ensureLoaded!(), controller.signal);
          epoch = tab.epoch;
          initializing = false;
          verify();
        }
        const releaseObservation = tab.view.prepareObservation?.();
        let release: (() => void) | undefined;
        try {
          release = capture ? await tab.view.prepareCapture?.(controller.signal) : undefined;
          verify();
          status(write ? "interacting" : readPhase);
          const result = await abortable(withFrameOperationSignal(controller.signal, () => work(tab, verify, controller.signal)), controller.signal);
          if (!write) verify();
          status("completed");
          return result;
        } finally { release?.(); releaseObservation?.(); }
      };
      return await (capture ? captureQueue.run(controller.signal, run) : run());
    } catch (error) {
      const kind = (error as { data?: { kind?: unknown } } | null)?.data?.kind;
      if (tab.operation?.id === requestId) status("failed", error instanceof Error ? diagnosticText(error.message).slice(0, 300) : "Browser operation failed", typeof kind === "string" ? kind : controller.signal.aborted ? "cancelled" : "host_error");
      if (write && controller.signal.aborted) throw browserFailure("cancelled", "operation interrupted; outcome may be unknown, do not replay");
      throw error;
    } finally {
      clearTimeout(timer);
      unsubscribe();
      offRevoke();
      if (requestId) pending.delete(requestId);
    }
  };

  // Navigation may already have reached the website. Cancellation releases the
  // host waiter, but never replays or rolls back a possibly completed write.
  const navigation = async <T>(params: Params, work: (signal: AbortSignal) => Promise<T>): Promise<T> => {
    const grantId = str(params, "grantId"), requestId = str(params, "requestId") || randomToken(8);
    grants.verify(grantId);
    if (pending.has(requestId)) throw browserFailure("cancelled", "duplicate request identity");
    const controller = new AbortController();
    pending.set(requestId, { grantId, abort: controller });
    const off = grants.onRevoke(grant => { if (grant.grantId === grantId) controller.abort(); });
    const timer = setTimeout(() => controller.abort(), Math.max(0, Math.min(num(params, "deadline", Date.now() + 15000) - Date.now(), 15000)));
    try { return await work(controller.signal); }
    catch (error) { if (controller.signal.aborted) throw browserFailure("cancelled", "navigation interrupted; outcome is unknown, do not replay"); throw error; }
    finally { clearTimeout(timer); off(); pending.delete(requestId); }
  };

  const calls: HostCallTable = {
    "host/browser.exportDiagnostics": params => {
      if (!deps.diagnosticExport) throw browserFailure("capability_unsupported", "browser diagnostic export is unavailable");
      return deps.diagnosticExport.read(str(params, "scope"));
    },
    "host/browser.preview.remember": params => {
      const tab = boundTab(params);
      const reference = { source: str(params, "source"), path: str(params, "path"), toolCallId: str(params, "toolCallId") };
      if (!validFileReference(reference)) throw new Error("invalid file reference");
      tab.fileReference = reference; tab.fileReferenceURL = tab.view.page.getURL();
      surfaces.setOperation(tab, { id: randomToken(8), phase: "completed" });
      return {};
    },
    "host/browser.capabilities": (params) => {
      grants.verify(str(params, "grantId"));
      return { capabilities: { query: true, wait: true, viewport: true, pointer: true, diagnostics: true, record: Boolean(deps.recorder) } };
    },
    "host/browser.record": async params => {
      if (!deps.recorder) throw browserFailure("capability_unsupported", "recording is unavailable");
      const action = str(params, "action");
      const tab = action === "start" ? agentTab(params) : boundTab(params);
      const request = (signal?: AbortSignal) => deps.recorder!.request(tab, str(params, "grantId"), action, str(params, "recordingId"), str(params, "directory"), num(params, "durationSeconds", 20), signal);
      const result = action === "start" ? await observe(params, false, (_tab, _verify, signal) => captureQueue.run(signal, () => request(signal)), true, true) : await request();
      return { recordingId: result.id, tabId: result.tabId, state: result.state, bytes: result.bytes, width: result.width, height: result.height, durationMs: result.durationMs, path: result.path, mime: result.path ? "video/webm" : undefined, error: result.error };
    },
    "host/browser.pointer": params => observe(params, true, async (tab, verify) => {
      const binding = documents.lookup(str(params, "documentToken"));
      if (!binding || binding.tabId !== tab.id || binding.epoch !== tab.epoch) throw staleReference("pointer requires the current documentToken");
      const action = str(params, "action");
      if (!["click", "hover", "drag"].includes(action)) throw new Error("invalid pointer action");
      const scale = tab.view.inputScale?.() ?? (tab.view.page.getZoomFactor() || 1);
      const point = async (ref: string) => {
        const resolved = await resolveRef(tab.view.page, binding, ref, false);
        if (!resolved.ok) throw staleReference(resolved.reason);
        const box = resolved.value.element;
        return { x: Math.round((box.x + box.width / 2) * scale), y: Math.round((box.y + box.height / 2) * scale) };
      };
      let from: { x: number; y: number };
      if (str(params, "ref")) from = await point(str(params, "ref"));
      else {
        const observation = observations.get(tab);
        const current = await geometry(tab);
        if (!observation || observation.token !== str(params, "observationToken") || observation.epoch !== tab.epoch || observation.revision !== tab.viewportRevision || current.scrollX !== observation.scrollX || current.scrollY !== observation.scrollY || current.width !== observation.width || current.height !== observation.height) throw staleReference("coordinate observation expired; take a new viewport screenshot");
        const x = num(params, "x", -1), y = num(params, "y", -1);
        if (x < 0 || y < 0 || x >= current.width || y >= current.height) throw new Error("coordinates fall outside the observed CSS viewport");
        from = { x: Math.round(x * scale), y: Math.round(y * scale) };
      }
      const to = action === "drag" ? await point(str(params, "targetRef")) : from;
      verify();
      surfaces.markAgentInput(tab);
      observations.delete(tab);
      const mouse = async (event: Electron.MouseInputEvent) => { verify(); if (tab.view.sendMouseInput) await tab.view.sendMouseInput(event, verify); else tab.view.page.sendInputEvent(event); };
      try {
        await mouse({ type: "mouseMove", ...from });
        if (action !== "hover") {
          await mouse({ type: "mouseDown", button: "left", clickCount: 1, ...from });
          if (action === "drag") await mouse({ type: "mouseMove", button: "left", movementX: to.x - from.x, movementY: to.y - from.y, ...to });
          await mouse({ type: "mouseUp", button: "left", clickCount: 1, ...to });
        }
        return { executed: true, documentToken: documents.rotate(str(params, "documentToken")) };
      } catch { return { executed: false, outcome: "unknown" }; }
    }, false, true),
    "host/browser.viewport": params => {
      const tab = agentTab(params);
      const action = str(params, "action");
      if (action === "set") surfaces.setViewport(tab.id, { width: num(params, "width"), height: num(params, "height"), scale: "fit" });
      else if (action === "reset") surfaces.setViewport(tab.id, null);
      else if (action !== "get") throw new Error("invalid viewport action");
      return { viewport: tab.viewport, revision: tab.viewportRevision, documentEpoch: tab.epoch };
    },
    "host/browser.diagnostics": params => {
      const tab = agentTab(params);
      if (!tab.view.diagnostics) throw browserFailure("capability_unsupported", "page diagnostic collection is unavailable");
      return tab.view.diagnostics.read(num(params, "after"), str(params, "kind"));
    },
    "host/browser.query": params => observe(params, true, tab => queryElements(tab, params, documents)),
    "host/browser.wait": params => observe(params, true, async (tab, verify, signal) => {
      const timeout = Math.min(8000, Math.max(1, num(params, "timeoutMs", 3000)));
      const deadline = Date.now() + timeout;
      const element = Boolean(str(params, "role") || str(params, "text") || str(params, "testId"));
      while (true) {
        verify();
        if (element) {
          const result = await queryElements(tab, { ...params, state: str(params, "state", "visible") }, documents);
          if (result.ambiguous) return { ...result, ready: false, reason: "ambiguous query; refine the locator" };
          if (result.state === true || (str(params, "state") === "hidden" && result.count === 0)) return { ...result, ready: true };
        } else {
          const ready = await tab.view.page.executeJavaScriptInIsolatedWorld(ISOLATED_WORLD, [{ code: scriptCall("pageReady", {}) }]) as { ready: boolean; url: string };
          if (ready.ready && (!str(params, "url") || str(params, "url") === ready.url)) return { ready: true, url: ready.url };
        }
        if (Date.now() >= deadline) throw browserFailure("page_not_ready", "wait condition did not become true before its deadline");
        await abortable(new Promise<void>(resolve => setTimeout(resolve, 50)), signal);
      }
    }, Boolean(str(params, "documentToken"))),
    "host/browser.grant": (params) => {
      const grant = grants.install({ grantId: str(params, "grantId"), taskId: str(params, "tabId"), sessionId: str(params, "sessionId"), diagnosticScope: str(params, "diagnosticScope") });
      deps.diagnosticExport?.bind(grant);
      return {};
    },
    "host/browser.revoke": (params) => {
      for (const request of pending.values()) if (request.grantId === str(params, "grantId")) request.abort.abort();
      const grant = grants.revoke(str(params, "grantId"));
      if (grant) for (const tab of surfaces.tabsForSession(grant.taskId, grant.sessionId)) documents.invalidateTab(tab.id);
      return {};
    },
    "host/browser.cancel": (params) => {
      const request = pending.get(str(params, "requestId"));
      if (request && request.grantId === str(params, "grantId")) request.abort.abort();
      return {};
    },
    "host/browser.tabs.list": (params) => {
      const grant = grants.verify(str(params, "grantId"));
      return { tabs: surfaces.tabsForSession(grant.taskId, grant.sessionId).map((tab) => hostTab(surfaces, tab)) };
    },
    "host/browser.tabs.open": params => navigation(params, async signal => {
      const grant = grants.verify(str(params, "grantId"));
      const tab = await surfaces.open(str(params, "url"), { taskId: grant.taskId, sessionId: grant.sessionId, temporary: bool(params, "temporary") }, signal);
      try { grants.verifyTab(str(params, "grantId"), tab.taskId, tab.sessionId); if (surfaces.get(tab.id) !== tab || tab.mode !== "agent") throw new Error("page ownership changed"); }
      catch {
        if (surfaces.get(tab.id) === tab && tab.mode === "agent") surfaces.close(tab.id);
        throw browserFailure("cancelled", "open was dispatched but its task changed; outcome is unknown");
      }
      return hostTab(surfaces, tab);
    }),
    "host/browser.tabs.navigate": params => navigation(params, async signal => {
      // allowHuman is emitted only by the renderer's explicit file-refresh
      // RPC. Agent browser tools never set it, so takeover still blocks them.
      const tab = bool(params, "allowHuman") ? boundTab(params) : agentTab(params);
      const action = str(params, "action");
      const target: BrowserNavigateTarget = action === "back" || action === "forward" || action === "reload" ? { action } : { url: str(params, "url") };
      await surfaces.navigate(tab.id, target, signal);
      try { grants.verifyTab(str(params, "grantId"), tab.taskId, tab.sessionId); if (surfaces.get(tab.id) !== tab || !bool(params, "allowHuman") && tab.mode !== "agent") throw new Error("page ownership changed"); }
      catch { throw browserFailure("cancelled", "navigation was dispatched but its task changed; outcome is unknown"); }
      return hostTab(surfaces, tab);
    }),
    "host/browser.tabs.close": (params) => {
      const tab = boundTab(params);
      documents.invalidateTab(tab.id);
      downloads.forgetTab(tab.id);
      surfaces.close(tab.id);
      return {};
    },
    "host/browser.snapshot": (params) => observe(params, true, async (tab, verify) => {
      const result = await snapshot(tab, str(params, "selector"), verify);
      verify();
      if (deps.snapshot) return result;
      const viewport = await geometry(tab);
      verify();
      return { ...result, observation: { url: tab.view.page.getURL(), timeMs: Date.now(), documentEpoch: tab.epoch, viewportRevision: tab.viewportRevision, cssWidth: viewport.width, cssHeight: viewport.height } };
    }),
    "host/browser.act": (params): Promise<ActResult> => observe(params, true, (tab, verify) => {
      const directory = str(params, "directory");
      if (directory !== "") downloads.setTaskDirectory(tab.taskId, directory);
      return deps.actions.act(
        tab,
        {
          operationId: str(params, "operationId"),
          tabId: tab.id,
          documentToken: str(params, "documentToken"),
          action: str(params, "action"),
          ref: str(params, "ref"),
          text: str(params, "text"),
          keys: str(params, "keys"),
          options: strList(params, "options"),
          files: strList(params, "files"),
          submit: bool(params, "submit"),
          deltaX: num(params, "deltaX"),
          deltaY: num(params, "deltaY"),
        },
        verify,
      );
    }, false, true),
    "host/browser.screenshot": (params) => observe(params, true, async (tab, verify) => {
      const directory = str(params, "directory");
      if (directory !== "") downloads.setTaskDirectory(tab.taskId, directory);
      if (deps.screenshot) return screenshot(tab, { ref: str(params, "ref"), fullPage: bool(params, "fullPage"), directory }, verify);
      const before = await geometry(tab);
      const result = await screenshot(tab, { ref: str(params, "ref"), fullPage: bool(params, "fullPage"), directory }, verify);
      const observed = { ...result, observation: { url: tab.view.page.getURL(), timeMs: Date.now(), documentEpoch: tab.epoch, viewportRevision: tab.viewportRevision, cssWidth: before.width, cssHeight: before.height } };
      try {
      verify();
      if (!str(params, "ref") && !bool(params, "fullPage")) {
        const after = await geometry(tab);
        if (JSON.stringify(before) !== JSON.stringify(after)) throw staleReference("page geometry changed during capture");
        const token = randomToken();
        observations.set(tab, { ...after, token, epoch: tab.epoch, revision: tab.viewportRevision });
        return { ...observed, observationToken: token, cssWidth: after.width, cssHeight: after.height };
      }
      return observed;
      } catch (error) { await rm(result.path, { force: true }); throw error; }
    }, true, false, "capturing"),
    "host/browser.downloads": async (params): Promise<{ downloads: HostDownload[] }> => {
      const tab = boundTab(params);
      return { downloads: await downloads.wait(tab.id, num(params, "waitForMs")) };
    },
  };
  for (const [method, call] of Object.entries(calls)) {
    if (method === "host/browser.exportDiagnostics" || method === "host/browser.grant") continue;
    calls[method] = async params => {
      let scope = "";
      try { scope = grants.verify(str(params, "grantId")).diagnosticScope ?? ""; } catch { /* Preserve the handler's original error semantics. */ }
      const request = { requestId: str(params, "requestId"), operationId: str(params, "operationId"), tabId: str(params, "tabId"), method };
      const started = performance.now();
      const emit = (event: import("./diagnosticContext.js").BrowserRequestTrace) => deps.diagnosticExport?.trace(scope, event);
      return withBrowserDiagnosticRequest(request, emit, async () => {
        emit({ ...request, phase: "started", elapsedMs: 0 });
        try {
          const result = await call(params);
          emit({ ...request, tabId: request.tabId || (typeof result === "object" && result !== null && "id" in result && typeof result.id === "string" ? result.id : ""), phase: "completed", elapsedMs: Math.round(performance.now() - started) });
          return result;
        } catch (error) {
          const kind = (error as { data?: { kind?: string } } | null)?.data?.kind;
          emit({ ...request, phase: "failed", elapsedMs: Math.round(performance.now() - started), errorKind: kind ?? "host_error" });
          throw error;
        }
      });
    };
  }
  return calls;
}
