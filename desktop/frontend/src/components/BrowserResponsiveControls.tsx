import type { BrowserTabView } from "../lib/browserHost";
import { useBrowserPanelStore } from "../lib/browserPanelStore";
import { useI18n } from "../lib/i18n";
import { useState } from "react";
import { browserElementDrafts } from "../lib/browserElementDrafts";

export function BrowserResponsiveControls({ tab }: { tab: BrowserTabView }) {
  const chinese = useI18n().locale !== "en";
  const viewport = tab.viewport;
  const [picking, setPicking] = useState(false);
  const pick = async () => {
    const { host, notify } = useBrowserPanelStore.getState();
    const target = browserElementDrafts.getState().target;
    if (!target || !host?.pickElement) { notify(chinese ? "当前任务暂不能接收元素附件" : "The current task cannot receive an element attachment yet"); return; }
    if (tab.taskId !== "user" && (tab.taskId !== target.taskId || tab.sessionId && tab.sessionId !== target.sessionId)) { notify(chinese ? "请切回此页面所属的任务" : "Return to the task that owns this page"); return; }
    setPicking(true);
    try {
      const selection = await host.pickElement(tab.id);
      if (selection) browserElementDrafts.getState().add(target.taskId, target.sessionId, selection);
    } catch (error) { notify(String(error)); }
    finally { setPicking(false); }
  };
  const set = (next: NonNullable<BrowserTabView["viewport"]> | null) => {
    const { host, notify } = useBrowserPanelStore.getState();
    void host?.setViewport?.(tab.id, next).catch(error => notify(String(error)));
  };
  return <details className="browser-responsive">
    <summary>{chinese ? "响应式" : "Responsive"}{viewport ? ` · ${viewport.width}×${viewport.height}` : ""}</summary>
    <div className="browser-responsive__controls">
      {tab.restorePreview && <button type="button" className="btn btn--small" onClick={() => { const { host, notify } = useBrowserPanelStore.getState(); void host?.restorePreview?.(tab.id).catch(error => notify(String(error))); }}>{chinese ? "重新授权并打开预览" : "Reauthorize and reopen preview"}</button>}
      <button type="button" className="btn btn--small" disabled={picking} onClick={() => void pick()}>{picking ? (chinese ? "点击元素 · Esc 退出" : "Click an element · Esc to cancel") : (chinese ? "选择元素加入聊天" : "Select element for chat")}</button>
      <select aria-label={chinese ? "视口预设" : "Viewport preset"} value={viewport ? `${viewport.width}x${viewport.height}` : "natural"} onChange={event => {
        if (event.target.value === "natural") set(null);
        else if (event.target.value !== "custom") { const [width, height] = event.target.value.split("x").map(Number); set({ width, height, scale: viewport?.scale ?? "fit" }); }
      }}>
        <option value="natural">{chinese ? "自然尺寸" : "Natural"}</option>
        <option value="393x852">{chinese ? "手机" : "Phone"} 393×852</option>
        <option value="768x1024">{chinese ? "平板" : "Tablet"} 768×1024</option>
        <option value="1280x720">{chinese ? "桌面" : "Desktop"} 1280×720</option>
        {viewport && !["393x852", "768x1024", "1280x720"].includes(`${viewport.width}x${viewport.height}`) && <option value={`${viewport.width}x${viewport.height}`}>{chinese ? "自定义" : "Custom"}</option>}
      </select>
      <form key={`${tab.id}:${viewport?.width}:${viewport?.height}`} onSubmit={event => {
        event.preventDefault(); const data = new FormData(event.currentTarget);
        set({ width: Number(data.get("width")), height: Number(data.get("height")), scale: viewport?.scale ?? "fit" });
      }}>
        <input name="width" aria-label={chinese ? "CSS 宽度" : "CSS width"} type="number" min="320" max="3840" defaultValue={viewport?.width ?? 1280} required /> ×
        <input name="height" aria-label={chinese ? "CSS 高度" : "CSS height"} type="number" min="320" max="2160" defaultValue={viewport?.height ?? 720} required />
        <button type="submit" className="btn btn--small">{chinese ? "应用" : "Apply"}</button>
      </form>
      {viewport && <>
        <button type="button" className="btn btn--small" disabled={viewport.width > 2160} onClick={() => set({ ...viewport, width: viewport.height, height: viewport.width })}>{chinese ? "旋转" : "Rotate"}</button>
        <select aria-label={chinese ? "显示比例" : "Display scale"} value={viewport.scale} onChange={event => set({ ...viewport, scale: event.target.value === "fit" ? "fit" : Number(event.target.value) })}>
          <option value="fit">Fit</option>{[0.5, 0.75, 1, 1.25, 1.5, 2].map(scale => <option key={scale} value={scale}>{scale * 100}%</option>)}
        </select>
      </>}
    </div>
  </details>;
}
