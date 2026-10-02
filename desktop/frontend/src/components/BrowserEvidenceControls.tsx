import { useEffect, useState } from "react";
import type { DesktopBrowserHost } from "../lib/browserHost";
import { useBrowserPanelStore } from "../lib/browserPanelStore";
import { useI18n } from "../lib/i18n";

type Recording = Awaited<ReturnType<NonNullable<DesktopBrowserHost["record"]>>>;
export function BrowserEvidenceControls({ tabId }: { tabId: string }) {
  const chinese = useI18n().locale !== "en";
  const [recording, setRecording] = useState<Recording>(null);
  const [busy, setBusy] = useState(false);
  const [diagnostics, setDiagnostics] = useState<string | null>(null);
  const [capture, setCapture] = useState<{ path: string; width: number; height: number } | null>(null);
  useEffect(() => {
    let disposed = false;
    const poll = async () => {
      try { const result = await useBrowserPanelStore.getState().host?.record?.(tabId, "status"); if (!disposed) setRecording(result ?? null); }
      catch { /* Availability is reported by the explicit action. */ }
    };
    void poll(); const timer = setInterval(() => void poll(), 1000);
    return () => { disposed = true; clearInterval(timer); };
  }, [tabId]);
  const active = recording && ["preparing", "recording", "finalizing"].includes(recording.state);
  const run = async (action: "start" | "stop" | "cancel") => {
    const { host, notify } = useBrowserPanelStore.getState();
    if (!host?.record) { notify(chinese ? "当前宿主不支持录制" : "Recording is unavailable"); return; }
    setBusy(true);
    try { setRecording(await host.record(tabId, action)); } catch (error) { notify(String(error)); }
    finally { setBusy(false); }
  };
  return <div className="browser-evidence">
    <button type="button" className="btn btn--small" onClick={() => {
      const { host, notify } = useBrowserPanelStore.getState();
      void host?.screenshot?.(tabId).then(setCapture).catch(error => notify(String(error)));
    }}>{chinese ? "截图" : "Screenshot"}</button>
    {capture && <button type="button" className="btn btn--small" title={capture.path} onClick={() => { void navigator.clipboard.writeText(capture.path); }}>{capture.width}×{capture.height} · {chinese ? "复制路径" : "Copy path"}</button>}
    <button type="button" className="btn btn--small" title={chinese ? "关闭面板后也可通过 View 菜单或 Ctrl/⌘+Shift+R 停止录制" : "Stop from the View menu or Ctrl/⌘+Shift+R even when this panel is closed"} disabled={busy} onClick={() => void run(active ? "stop" : "start")}>{active ? (chinese ? "停止录制" : "Stop recording") : (chinese ? "录制 · 20 秒" : "Record · 20 s")}</button>
    {active && <button type="button" className="btn btn--small" onClick={() => void run("cancel")}>{chinese ? "取消录制" : "Cancel recording"}</button>}
    {recording && <span role="status" title={recording.path ?? recording.error}>{recording.state} · {Math.ceil(recording.bytes / 1024)} KiB</span>}
    {recording?.path && <button type="button" className="btn btn--small" onClick={() => { void navigator.clipboard.writeText(recording.path!); }}>{chinese ? "复制产物路径" : "Copy artifact path"}</button>}
    <button type="button" className="btn btn--small" onClick={() => {
      const { host, notify } = useBrowserPanelStore.getState();
      void host?.diagnostics?.(tabId).then(result => setDiagnostics(result.available ? JSON.stringify(result.entries ?? [], null, 2) : (chinese ? "诊断不可用" : "Diagnostics unavailable"))).catch(error => notify(String(error)));
    }}>{chinese ? "页面诊断" : "Page diagnostics"}</button>
    {diagnostics !== null && <details open><summary onClick={() => setDiagnostics(null)}>{chinese ? "关闭诊断" : "Close diagnostics"}</summary><pre>{diagnostics}</pre></details>}
  </div>;
}
