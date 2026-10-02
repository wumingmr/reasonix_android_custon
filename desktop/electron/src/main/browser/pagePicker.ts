/// <reference lib="dom" />
import { pageRegistry } from "./pageSemantic.js";

export function pageCancelPicker(): void {
  (window as unknown as { __reasonixCancelPicker?: () => void }).__reasonixCancelPicker?.();
}

export async function pagePickElement(input: { key: string }): Promise<unknown> {
  const registry = pageRegistry(input.key);
  const host = window as unknown as { __reasonixCancelPicker?: () => void };
  host.__reasonixCancelPicker?.();
  return new Promise(resolve => {
    const overlay = document.createElement("div");
    overlay.style.cssText = "position:fixed;pointer-events:none;z-index:2147483647;border:2px solid #4d8dff;background:rgba(77,141,255,.12);box-sizing:border-box;display:none";
    document.documentElement.append(overlay);
    let target: Element | null = null;
    let finished = false;
    const finish = (result: unknown) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer); overlay.remove();
      window.removeEventListener("pointermove", move, true);
      window.removeEventListener("click", click, true);
      window.removeEventListener("keydown", key, true);
      for (const name of ["pointerdown", "mousedown", "mouseup"]) window.removeEventListener(name, block, true);
      if (host.__reasonixCancelPicker === cancel) delete host.__reasonixCancelPicker;
      resolve(result);
    };
    const cancel = () => finish(null);
    const block = (event: Event) => { if (event.isTrusted) { event.preventDefault(); event.stopImmediatePropagation(); } };
    const move = (event: PointerEvent) => {
      if (!event.isTrusted) return;
      target = event.composedPath().find(node => node instanceof Element && node !== overlay) as Element | undefined ?? null;
      if (!target) return;
      const box = target.getBoundingClientRect();
      Object.assign(overlay.style, { display: "block", left: `${box.x}px`, top: `${box.y}px`, width: `${box.width}px`, height: `${box.height}px` });
    };
    const click = (event: MouseEvent) => {
      if (!event.isTrusted) return;
      block(event);
      const element = event.composedPath().find(node => node instanceof Element && node !== overlay) as Element | undefined ?? target;
      if (!element) return;
      const box = element.getBoundingClientRect(), style = getComputedStyle(element);
      const sensitive = element instanceof HTMLInputElement && element.type === "password";
      finish({ role: registry.kernel.utils.getAriaRole(element), name: registry.kernel.utils.getElementAccessibleNameText(element, false).slice(0, 160), text: sensitive ? "" : (element.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 300), tag: element.tagName.toLowerCase(), box: { x: box.x, y: box.y, width: box.width, height: box.height }, style: { display: style.display, color: style.color, backgroundColor: style.backgroundColor, fontSize: style.fontSize }, historical: true });
    };
    const key = (event: KeyboardEvent) => { if (event.isTrusted && event.key === "Escape") { block(event); cancel(); } };
    const timer = setTimeout(cancel, 90_000);
    host.__reasonixCancelPicker = cancel;
    window.addEventListener("pointermove", move, true);
    window.addEventListener("click", click, true);
    window.addEventListener("keydown", key, true);
    for (const name of ["pointerdown", "mousedown", "mouseup"]) window.addEventListener(name, block, true);
  });
}
