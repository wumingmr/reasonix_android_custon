import type { IpcMain, IpcMainEvent, IpcMainInvokeEvent } from "electron";
import {
  IPC,
  type BrowserLayoutRect,
  type BrowserNavigateTarget,
  type BrowserOpenOptions,
  type BrowserTabView,
  type IpcResult,
  type ServiceState,
  type WindowBounds,
  type WindowTheme,
} from "../shared/ipc.js";
import { isAllowedCommand, type LoadedContract } from "./contract.js";
import { errorText, type Logger } from "./log.js";
import { bool, finite, record, str, type Params } from "./params.js";
import { RpcError } from "./rpc.js";
import type { GraphicsSettingsStore } from "./graphics.js";
import type { BrowserControlApi } from "./browserControlHost.js";
import type { PerformanceHost } from "./performanceHost.js";

export interface RendererWindowApi {
  isTrustedSender(sender: IpcMainEvent["sender"], frame: IpcMainEvent["senderFrame"]): boolean;
  minimise(): void;
  toggleMaximise(): void;
  isMaximised(): boolean;
  close(): void;
  bounds(): WindowBounds;
  setTheme(theme: WindowTheme): void;
  setBackgroundColour(r: number, g: number, b: number, a: number): void;
  getAppZoom(): Promise<number>;
  setAppZoom(factor: number): Promise<number>;
  resetAppZoom(): Promise<number>;
}

// The user-driven browser panel: no grant is involved because the user is
// the one acting, but every call is still gated on the trusted sender.
export interface BrowserRendererApi {
  list(): BrowserTabView[];
  open(url: string, options: Required<BrowserOpenOptions>): Promise<BrowserTabView>;
  close(tabId: string): void;
  activate(tabId: string | null): void;
  navigate(tabId: string, target: BrowserNavigateTarget): Promise<void>;
  setZoom(tabId: string, factor: number): void;
  setViewport?(tabId: string, viewport: { width: number; height: number; scale: "fit" | number } | null): void;
  pickElement?(tabId: string): Promise<unknown>;
  record?(tabId: string, action: string): Promise<unknown>;
  diagnostics?(tabId: string): unknown;
  screenshot?(tabId: string): Promise<unknown>;
  restorePreview?(tabId: string): Promise<void>;
  toggleDevTools(tabId: string): void;
  resume(tabId: string): void;
  takeover(tabId: string): void;
  setLayout(rect: BrowserLayoutRect | null): void;
  setOverlay(active: boolean): void;
}

export interface RendererIpcDeps {
  ipcMain: IpcMain;
  contract: LoadedContract;
  window: RendererWindowApi;
  invoke(method: string, args: unknown[]): Promise<unknown>;
  serviceState(): ServiceState;
  processDiagnostics?(): unknown;
  performance?: PerformanceHost;
  clipboard: { writeText(text: string): Promise<void> | void; readText(): Promise<string> | string };
  graphics?: GraphicsSettingsStore;
  browserControl?: BrowserControlApi;
  openExternal(url: string): Promise<void>;
  browser?: BrowserRendererApi;
  log: Logger;
}

const NAVIGATE_ACTIONS = new Set(["back", "forward", "reload", "stop"]);

function diagnosticRequestId(value: unknown): string | undefined {
  if (value === undefined) return undefined;
  if (typeof value !== "string" || !/^[a-zA-Z0-9-]{1,96}$/.test(value)) throw new Error("invalid diagnostic request identity");
  return value;
}

const TRANSCRIPT_DIAGNOSTIC_EVENTS = new Set(["failure", "summary", "recovered", "stopped"]);
const TRANSCRIPT_DIAGNOSTIC_STAGES = new Set(["none", "baseline_read", "baseline_validate", "snapshot_install", "delta_read", "delta_validate", "delta_apply"]);
const TRANSCRIPT_DIAGNOSTIC_REASONS = new Set([
  "transport_rejected", "protocol_version", "snapshot_missing", "history_not_ready", "revision_regressed",
  "revision_gap", "business_gap", "frame_cut_mismatch", "sampling_identity_missing", "sampling_gap",
  "settlement_not_committed", "settlement_identity_mismatch", "resync_required", "consumer_error",
  "service_stopping", "unknown",
]);
const TRANSCRIPT_DIAGNOSTIC_TRANSPORTS = new Set(["local", "remote"]);
const TRANSCRIPT_DIAGNOSTIC_ERROR_TYPES = new Set(["classified", "error", "string", "object", "unknown"]);
const TRANSCRIPT_DIAGNOSTIC_KEYS = new Set([
  "kind", "event", "stage", "reason", "transport", "errorType", "revision", "commit", "attempts", "failures", "durationMs",
]);

type RendererTranscriptDiagnostic = {
  kind: "transcript";
  event: string;
  stage: string;
  reason: string;
  transport: string;
  errorType: string;
  revision: number;
  commit: number;
  attempts: number;
  failures: number;
  durationMs: number;
};

function diagnosticCount(input: Params, key: string): number {
  const value = input[key];
  if (!Number.isSafeInteger(value) || (value as number) < 0 || (value as number) > 1_000_000_000_000) {
    throw new Error(`invalid renderer diagnostic ${key}`);
  }
  return value as number;
}

export function parseRendererDiagnostic(value: unknown): RendererTranscriptDiagnostic {
  let encoded: string | undefined;
  try { encoded = JSON.stringify(value); } catch { throw new Error("invalid renderer diagnostic payload"); }
  if (typeof encoded !== "string") throw new Error("invalid renderer diagnostic payload");
  if (Buffer.byteLength(encoded, "utf8") > 2048) throw new Error("renderer diagnostic payload too large");
  const input = record(value);
  if (Object.keys(input).some(key => !TRANSCRIPT_DIAGNOSTIC_KEYS.has(key))) throw new Error("invalid renderer diagnostic field");
  const event = str(input, "event"), stage = str(input, "stage"), reason = str(input, "reason"), transport = str(input, "transport");
  const errorType = str(input, "errorType");
  if (input.kind !== "transcript" || !TRANSCRIPT_DIAGNOSTIC_EVENTS.has(event) || !TRANSCRIPT_DIAGNOSTIC_STAGES.has(stage)
    || !TRANSCRIPT_DIAGNOSTIC_REASONS.has(reason) || !TRANSCRIPT_DIAGNOSTIC_TRANSPORTS.has(transport)
    || !TRANSCRIPT_DIAGNOSTIC_ERROR_TYPES.has(errorType)) {
    throw new Error("invalid renderer diagnostic value");
  }
  return {
    kind: "transcript", event, stage, reason, transport, errorType,
    revision: diagnosticCount(input, "revision"),
    commit: diagnosticCount(input, "commit"),
    attempts: diagnosticCount(input, "attempts"),
    failures: diagnosticCount(input, "failures"),
    durationMs: diagnosticCount(input, "durationMs"),
  };
}

export function parseNavigateTarget(value: unknown): BrowserNavigateTarget {
  const target = record(value);
  const action = str(target, "action");
  if (NAVIGATE_ACTIONS.has(action)) return { action: action as BrowserNavigateTarget["action"] };
  return { url: str(target, "url") };
}

export function parseLayout(value: unknown): BrowserLayoutRect | null {
  if (value === null || value === undefined) return null;
  const rect = record(value);
  return { x: finite(rect.x, Number.NaN), y: finite(rect.y, Number.NaN), width: finite(rect.width, Number.NaN), height: finite(rect.height, Number.NaN) };
}

const EXTERNAL_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);

export function isOpenableExternalURL(value: unknown): value is string {
  if (typeof value !== "string") return false;
  try {
    return EXTERNAL_PROTOCOLS.has(new URL(value).protocol);
  } catch {
    return false;
  }
}

export function registerRendererIpc(deps: RendererIpcDeps): void {
  const trusted = (event: IpcMainEvent | IpcMainInvokeEvent): boolean => {
    const ok = deps.window.isTrustedSender(event.sender, event.senderFrame);
    if (!ok) deps.log.warn(`rejected IPC from untrusted sender (webContents ${event.sender.id})`);
    return ok;
  };
  const handle = (channel: string, run: (...args: unknown[]) => Promise<unknown> | unknown) => {
    deps.ipcMain.handle(channel, async (event, ...args: unknown[]): Promise<IpcResult> => {
      if (!trusted(event)) return { ok: false, message: "untrusted sender" };
      try {
        return { ok: true, value: await run(...args) };
      } catch (error) {
        return { ok: false, message: errorText(error), ...(error instanceof RpcError ? {code:error.code,data:error.data} : {}) };
      }
    });
  };
  let diagnosticWindowStartedAt = 0;
  let diagnosticWindowCount = 0;
  let diagnosticDropped = 0;
  let diagnosticDropTimer: ReturnType<typeof setTimeout> | undefined;
  const reportDiagnosticDrops = () => {
    if (diagnosticDropped > 0) deps.log.warn(`renderer diagnostics rate limited dropped=${diagnosticDropped}`);
    diagnosticDropped = 0;
    diagnosticDropTimer = undefined;
  };

  deps.ipcMain.on(IPC.contract, (event) => {
    event.returnValue = trusted(event)
      ? { protocolVersion: deps.contract.protocolVersion, digest: deps.contract.digest, commands: [...deps.contract.commands] }
      : null;
  });

  handle(IPC.invoke, (method, args) => {
    if (!isAllowedCommand(deps.contract, method)) {
      throw new RpcError(-32601, `-32601 method not found: ${typeof method === "string" ? method : typeof method}`);
    }
    // Older renderers reached native title-bar controls through generated Go
    // bindings. Keep those command names compatible while the Electron shell
    // owns window lifetime and can accept repeated close requests after the Go
    // service has begun shutting down.
    if (method === "MinimiseMainWindow") return deps.window.minimise();
    if (method === "ToggleMaximiseMainWindow") return deps.window.toggleMaximise();
    if (method === "IsMainWindowMaximised") return deps.window.isMaximised();
    if (method === "CloseMainWindow") return deps.window.close();
    return deps.invoke(method, Array.isArray(args) ? args : []);
  });
  handle(IPC.serviceStateGet, () => deps.serviceState());
  handle(IPC.processDiagnostics, () => deps.processDiagnostics?.() ?? null);
  handle(IPC.captureRendererProfile, (id) => deps.performance?.captureRendererProfile(diagnosticRequestId(id)) ?? { status: "unavailable" });
  handle(IPC.cancelRendererProfile, (id) => {
    const requestId = diagnosticRequestId(id);
    if (requestId) deps.performance?.cancelRendererProfile(requestId);
  });
  handle(IPC.exportHeapSnapshot, () => deps.performance?.exportHeapSnapshot() ?? { status: "unavailable" });
  handle(IPC.rendererDiagnostic, (value) => {
    const diagnostic = parseRendererDiagnostic(value);
    const now = Date.now();
    if (now - diagnosticWindowStartedAt >= 1000) {
      if (diagnosticDropTimer) clearTimeout(diagnosticDropTimer);
      reportDiagnosticDrops();
      diagnosticWindowStartedAt = now;
      diagnosticWindowCount = 0;
    }
    if (diagnosticWindowCount >= 10) {
      diagnosticDropped++;
      if (!diagnosticDropTimer) {
        diagnosticDropTimer = setTimeout(reportDiagnosticDrops, 1000);
        diagnosticDropTimer.unref();
      }
      return;
    }
    diagnosticWindowCount++;
    const service = deps.serviceState();
    deps.log.info(
      `renderer transcript event=${diagnostic.event} stage=${diagnostic.stage} reason=${diagnostic.reason} type=${diagnostic.errorType} transport=${diagnostic.transport} revision=${diagnostic.revision} commit=${diagnostic.commit} attempts=${diagnostic.attempts} failures=${diagnostic.failures} duration_ms=${diagnostic.durationMs} service=${service.phase} generation=${service.generation}`,
    );
  });
  handle(IPC.openExternal, (url) => {
    if (!isOpenableExternalURL(url)) throw new Error(`refusing to open ${typeof url === "string" ? url : typeof url}`);
    return deps.openExternal(url);
  });
  handle(IPC.clipboardWrite, async (text) => {
    await deps.clipboard.writeText(typeof text === "string" ? text : "");
    return true;
  });
  handle(IPC.clipboardRead, () => deps.clipboard.readText());
  handle(IPC.windowMinimise, () => deps.window.minimise());
  handle(IPC.windowToggleMaximise, () => deps.window.toggleMaximise());
  handle(IPC.windowIsMaximised, () => deps.window.isMaximised());
  handle(IPC.windowClose, () => deps.window.close());
  handle(IPC.windowGetBounds, () => deps.window.bounds());
  handle(IPC.windowSetTheme, (theme) => deps.window.setTheme(theme === "light" || theme === "dark" ? theme : "system"));
  handle(IPC.windowSetBackground, (r, g, b, a) => deps.window.setBackgroundColour(finite(r), finite(g), finite(b), finite(a, 255)));
  handle(IPC.appZoomGet, () => deps.window.getAppZoom());
  handle(IPC.appZoomSet, (factor) => deps.window.setAppZoom(finite(factor, Number.NaN)));
  handle(IPC.appZoomReset, () => deps.window.resetAppZoom());
  handle(IPC.graphicsGet, () => deps.graphics?.current ?? { hardwareAcceleration: true, startupEnabled: true, override: "none", restartRequired: false, writable: false, warning: null });
  handle(IPC.graphicsSet, (enabled) => {
    if (typeof enabled !== "boolean") throw new Error("hardwareAcceleration must be boolean");
    if (!deps.graphics) throw new Error("graphics settings unavailable");
    return deps.graphics.setHardwareAcceleration(enabled);
  });

  const browserControl = deps.browserControl;
  const browserFlag = (value: unknown, name: string): boolean => {
    if (typeof value !== "boolean") throw new Error(`${name} must be boolean`);
    return value;
  };
  const requireBrowserControl = (): BrowserControlApi => {
    if (!browserControl) throw new Error("browser control settings unavailable");
    return browserControl;
  };
  handle(IPC.browserControlGet, () => browserControl?.state() ?? null);
  handle(IPC.browserControlSetEnabled, (enabled) => requireBrowserControl().setControlEnabled(browserFlag(enabled, "controlEnabled")));
  handle(IPC.browserControlSetIgnoreCertificateErrors, (enabled) =>
    requireBrowserControl().setIgnoreCertificateErrors(browserFlag(enabled, "ignoreCertificateErrors")),
  );
  handle(IPC.browserControlClearCache, () => requireBrowserControl().clearCache());
  handle(IPC.browserControlClearAll, () => requireBrowserControl().clearAllData());
  handle(IPC.browserControlImportChrome, () => requireBrowserControl().importChromeLogin());

  const browser = deps.browser;
  if (!browser) return;
  const tabId = (value: unknown): string => {
    if (typeof value !== "string" || value === "") throw new Error("tabId must be a non-empty string");
    return value;
  };
  handle(IPC.browserList, () => browser.list());
  handle(IPC.browserRecord, (id, action) => {
    if (!browser.record || typeof action !== "string" || !["start", "status", "stop", "cancel"].includes(action)) throw new Error("invalid recording operation");
    return browser.record(tabId(id), action);
  });
  handle(IPC.browserDiagnostics, id => browser.diagnostics?.(tabId(id)) ?? { available: false });
  handle(IPC.browserScreenshot, id => { if (!browser.screenshot) throw new Error("screenshot unavailable"); return browser.screenshot(tabId(id)); });
  handle(IPC.browserRestorePreview, id => { if (!browser.restorePreview) throw new Error("preview recovery unavailable"); return browser.restorePreview(tabId(id)); });
  handle(IPC.browserPickElement, id => {
    if (!browser.pickElement) throw new Error("element picker is unavailable");
    return browser.pickElement(tabId(id));
  });
  handle(IPC.browserOpen, (url, options) => {
    if (typeof url !== "string") throw new Error("url must be a string");
    const opts = record(options);
    return browser.open(url, { temporary: bool(opts, "temporary"), taskId: str(opts, "taskId", "user") || "user" });
  });
  handle(IPC.browserClose, (id) => browser.close(tabId(id)));
  handle(IPC.browserActivate, (id) => browser.activate(id === null || id === undefined ? null : tabId(id)));
  handle(IPC.browserNavigate, (id, target) => browser.navigate(tabId(id), parseNavigateTarget(target)));
  handle(IPC.browserSetZoom, (id, factor) => browser.setZoom(tabId(id), finite(factor, Number.NaN)));
  handle(IPC.browserSetViewport, (id, value) => {
    if (!browser.setViewport) throw new Error("viewport controls are unavailable");
    const viewport = record(value);
    browser.setViewport(tabId(id), value === null ? null : { width: finite(viewport.width, Number.NaN), height: finite(viewport.height, Number.NaN), scale: viewport.scale === "fit" ? "fit" : finite(viewport.scale, Number.NaN) });
  });
  handle(IPC.browserToggleDevTools, (id) => browser.toggleDevTools(tabId(id)));
  handle(IPC.browserResume, (id) => browser.resume(tabId(id)));
  handle(IPC.browserUserTakeover, (id) => browser.takeover(tabId(id)));
  handle(IPC.browserSetLayout, (rect) => browser.setLayout(parseLayout(rect)));
  handle(IPC.browserSetOverlay, (active) => browser.setOverlay(active === true));
}
