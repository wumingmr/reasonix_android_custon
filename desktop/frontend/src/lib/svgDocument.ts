// Recognition helpers for model-authored SVG. They are deliberately shallow:
// they decide whether a fenced block is worth handing to the host, never
// whether the markup is safe. The host's sanitizer owns that verdict.

/**
 * True when the text *starts* like a single SVG document. A body that only
 * looks like one — mixed HTML, several roots, a truncated fragment — passes
 * here and is still refused by the host's strict parse, which is why this is
 * used to skip work rather than to authorize a preview.
 */
export function looksLikeSvgDocument(value: string): boolean {
  const head = value.replace(/^﻿/, "").trimStart();
  let offset = 0;
  const skipWhitespace = () => {
    while (offset < head.length && /\s/u.test(head[offset] ?? "")) offset += 1;
  };
  if (head.slice(0, 5).toLowerCase() === "<?xml") {
    const end = head.indexOf("?>", 5);
    if (end < 0) return false;
    offset = end + 2;
    skipWhitespace();
  }
  while (head.startsWith("<!--", offset)) {
    const end = head.indexOf("-->", offset + 4);
    if (end < 0) return false;
    offset = end + 3;
    skipWhitespace();
  }
  return head.slice(offset, offset + 4).toLowerCase() === "<svg"
    && /[\s/>]/u.test(head[offset + 4] ?? "");
}

/** The picture's own ratio, so the block reserves space before the image loads. */
export function svgAspectRatio(svg: string): number | undefined {
  const root = /<svg[^>]*>/i.exec(svg)?.[0];
  if (!root) return undefined;
  const viewBox = /viewBox\s*=\s*"([^"]*)"/i.exec(root)?.[1]?.trim().split(/[\s,]+/).map(Number);
  if (viewBox && viewBox.length === 4 && viewBox.every(Number.isFinite) && viewBox[2] > 0 && viewBox[3] > 0) {
    return viewBox[2] / viewBox[3];
  }
  const width = unitless(/width\s*=\s*"([^"]*)"/i.exec(root)?.[1]);
  const height = unitless(/height\s*=\s*"([^"]*)"/i.exec(root)?.[1]);
  return width && height ? width / height : undefined;
}

function unitless(raw?: string): number | undefined {
  if (!raw) return undefined;
  const match = /^\s*([0-9]*\.?[0-9]+)\s*(?:px)?\s*$/i.exec(raw);
  const value = match ? Number(match[1]) : NaN;
  return Number.isFinite(value) && value > 0 ? value : undefined;
}
