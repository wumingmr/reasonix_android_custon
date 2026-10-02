/// <reference lib="dom" />

// DOM geometry can already reflect a resize while Chromium is still routing
// input using the preceding compositor frame. Observe two animation frames
// before dispatch, and reject rather than silently retarget a moving point.
export function pageInputReady(input: { x: number; y: number }): Promise<boolean> {
  const target = document.elementFromPoint(input.x, input.y);
  const sample = () => {
    const rect = target?.getBoundingClientRect();
    return JSON.stringify([innerWidth, innerHeight, scrollX, scrollY, rect?.x, rect?.y, rect?.width, rect?.height]);
  };
  const initial = sample();
  return new Promise(resolve => {
    let frame = 0, remaining = 2, finished = false;
    const finish = (ready: boolean) => {
      if (finished) return;
      finished = true;
      clearTimeout(timer);
      cancelAnimationFrame(frame);
      resolve(ready);
    };
    const timer = setTimeout(() => finish(false), 500);
    const observe = () => {
      if (sample() !== initial || document.elementFromPoint(input.x, input.y) !== target || target && !target.isConnected) { finish(false); return; }
      if (--remaining === 0) { finish(true); return; }
      frame = requestAnimationFrame(observe);
    };
    frame = requestAnimationFrame(observe);
  });
}

export function pageInputFrame(input: { x: number; y: number }) {
  let hit = document.elementFromPoint(input.x, input.y);
  while (hit?.shadowRoot) {
    const nested = hit.shadowRoot.elementFromPoint(input.x, input.y);
    if (!nested || nested === hit) break;
    hit = nested;
  }
  if (!(hit instanceof HTMLIFrameElement || hit instanceof HTMLFrameElement)) return null;
  const transform = getComputedStyle(hit).transform;
  if (transform && transform !== "none") {
    const matrix = new DOMMatrixReadOnly(transform);
    if (matrix.b !== 0 || matrix.c !== 0 || matrix.a <= 0 || matrix.d <= 0) return { unsupported: true };
  }
  const rect = hit.getBoundingClientRect();
  const index = Array.from({ length: window.length }, (_, i) => i).find(i => window.frames[i] === hit.contentWindow);
  if (index === undefined || !rect.width || !rect.height) return null;
  const x = (input.x - rect.x) * hit.offsetWidth / rect.width - hit.clientLeft;
  const y = (input.y - rect.y) * hit.offsetHeight / rect.height - hit.clientTop;
  if (x < 0 || y < 0 || x >= hit.clientWidth || y >= hit.clientHeight) return null;
  return { index, x, y };
}
