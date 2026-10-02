// Renderer view of the shell's browser surface manager. Mirrors the
// `window.reasonixDesktop.browser` preload API (docs/DESKTOP_BROWSER.md).
export type BrowserTabMode = "agent" | "human";

export interface BrowserTabView {
  restorePreview?: boolean;
  sessionId?: string;
  id: string;
  taskId: string;
  url: string;
  title: string;
  loading: boolean;
  canGoBack: boolean;
  canGoForward: boolean;
  temporary: boolean;
  mode: BrowserTabMode;
  epoch: number;
  zoom: number;
  viewport?: { width: number; height: number; scale: "fit" | number } | null;
  operation?: { id: string; phase: string; message?: string };
  error: { code: number; description: string } | null;
}

export type BrowserDownloadState = "progressing" | "completed" | "cancelled" | "interrupted";

export interface BrowserDownloadView {
  id: string;
  tabId: string;
  url: string;
  filename: string;
  path: string;
  state: BrowserDownloadState;
  received: number;
  total: number;
}

export interface BrowserLayoutRect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface BrowserNavigationTarget {
  url?: string;
  action?: "back" | "forward" | "reload" | "stop";
}

export interface DesktopBrowserHost {
  list(): Promise<BrowserTabView[]>;
  open(url: string, opts?: { temporary?: boolean; taskId?: string }): Promise<BrowserTabView>;
  close(tabId: string): Promise<void>;
  activate(tabId: string | null): Promise<void>;
  navigate(tabId: string, target: BrowserNavigationTarget): Promise<void>;
  setZoom(tabId: string, factor: number): Promise<void>;
  setViewport?(tabId: string, viewport: { width: number; height: number; scale: "fit" | number } | null): Promise<void>;
  pickElement?(tabId: string): Promise<{ taskId: string; sessionId: string; tabId: string; epoch: number; url: string; time: number; element: unknown } | null>;
  record?(tabId: string, action: "start" | "status" | "stop" | "cancel"): Promise<{ id: string; state: string; bytes: number; path?: string; error?: string } | null>;
  diagnostics?(tabId: string): Promise<{ available: boolean; entries?: unknown[] }>;
  screenshot?(tabId: string): Promise<{ path: string; width: number; height: number }>;
  restorePreview?(tabId: string): Promise<void>;
  toggleDevTools(tabId: string): Promise<void>;
  resume(tabId: string): Promise<void>;
  takeover(tabId: string): Promise<void>;
  setLayout(rect: BrowserLayoutRect | null): void;
  setOverlay(active: boolean): void;
  onTabs(cb: (tabs: BrowserTabView[]) => void): () => void;
  onDownload(cb: (download: BrowserDownloadView) => void): () => void;
}
