export type ElementLayoutSize = {
  width: number;
  height: number;
};

export const MAX_INITIAL_OVERLAY_MEASUREMENT_FRAMES = 8;

export function isElementExplicitlyHidden(element: HTMLElement): boolean {
  for (let current: HTMLElement | null = element; current; current = current.parentElement) {
    if (current.hidden || current.hasAttribute("inert")) return true;
    const style = window.getComputedStyle(current);
    if (style.display === "none" || style.visibility === "hidden" || style.visibility === "collapse") return true;
  }
  return false;
}

export function validAnchorRect(element: HTMLElement | null): DOMRect | null {
  if (!element?.isConnected || isElementExplicitlyHidden(element)) return null;
  const rect = element.getBoundingClientRect();
  if (
    !Number.isFinite(rect.left) ||
    !Number.isFinite(rect.top) ||
    !Number.isFinite(rect.width) ||
    !Number.isFinite(rect.height) ||
    rect.width <= 0 ||
    rect.height <= 0
  ) return null;
  return rect;
}

// offsetWidth/offsetHeight are layout dimensions and do not include the
// popover's entry/exit transform. JSDOM and a few embedders may only expose a
// useful bounding rect, so retain that as a fallback.
export function elementLayoutSize(element: HTMLElement): ElementLayoutSize | null {
  const rect = element.getBoundingClientRect();
  const width = element.offsetWidth || rect.width;
  const height = element.offsetHeight || rect.height;
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return null;
  return { width, height };
}
