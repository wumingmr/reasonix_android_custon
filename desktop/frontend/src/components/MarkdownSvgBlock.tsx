// An SVG code block that renders as a picture.
//
// The model writes SVG as a fence far more often than as a file, so the block
// opens as a preview and keeps its source one click away. The markup is never
// injected into the app DOM: the host returns sanitized bytes, and those bytes
// become an <img> source, which is what keeps a model-authored script or
// event handler inert.
//
// A block that cannot be previewed — too large, too deeply nested, malformed,
// or not a single SVG root at all — simply stays source. Nothing here rewrites
// the answer text.

import { memo, useEffect, useMemo, useRef, useState } from "react";
import "./MarkdownSvgBlock.css";
import { Code2, Play } from "lucide-react";
import { CodeViewer } from "./CodeViewer";
import { CopyButton } from "./CopyButton";
import { app } from "../lib/bridge";
import { t } from "../lib/i18n";
import { svgAspectRatio } from "../lib/svgDocument";
import type { MarkdownSVGView } from "../generated/desktopContract.generated";

const PREVIEW_MAX_HEIGHT = "min(60vh, 32rem)";
const CACHE_BUDGET_BYTES = 4 << 20;

const cache = new Map<string, MarkdownSVGView>();
let cacheBytes = 0;

function cachedSanitize(value: string): MarkdownSVGView | undefined {
  return cache.get(value);
}

function remember(value: string, view: MarkdownSVGView): void {
  if (!cache.has(value)) {
    cacheBytes += value.length * 2;
    cache.set(value, view);
    while (cacheBytes > CACHE_BUDGET_BYTES && cache.size > 1) {
      const oldest = cache.keys().next().value as string;
      cacheBytes -= oldest.length * 2;
      cache.delete(oldest);
    }
  }
}

function imageSource(svg: string): string {
  if (typeof URL !== "undefined" && typeof URL.createObjectURL === "function") {
    return URL.createObjectURL(new Blob([svg], { type: "image/svg+xml" }));
  }
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
}

export const MarkdownSvgBlock = memo(function MarkdownSvgBlock({ value }: { value: string }) {
  const [view, setView] = useState<MarkdownSVGView | undefined>(() => cachedSanitize(value));
  const [mode, setMode] = useState<"preview" | "source">(() => cachedSanitize(value)?.ok ? "preview" : "source");
  // An explicit choice belongs to the reader: a later render must not undo it.
  const chosen = useRef(false);
  const [src, setSrc] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    const hit = cachedSanitize(value);
    if (hit) { setView(hit); return; }
    void app.SanitizeMarkdownSVG(value).then(next => {
      if (!live) return;
      remember(value, next);
      setView(next);
    }).catch(() => {
      if (live) setView({ ok: false, reason: "invalid" });
    });
    return () => { live = false; };
  }, [value]);

  useEffect(() => {
    if (!view?.ok || !view.svg) { setSrc(null); return; }
    const next = imageSource(view.svg);
    setSrc(next);
    // Release the picture this block is replacing, and the one it leaves behind
    // on unmount. A superseded sanitize result never reaches the DOM because
    // this effect replaces the source in the same commit.
    return () => { if (next.startsWith("blob:")) URL.revokeObjectURL(next); };
  }, [view]);

  useEffect(() => {
    if (chosen.current || !view) return;
    setMode(view.ok ? "preview" : "source");
  }, [view]);

  const select = (next: "preview" | "source") => { chosen.current = true; setMode(next); };
  const ratio = useMemo(() => (view?.ok && view.svg ? svgAspectRatio(view.svg) : undefined), [view]);
  const previewable = Boolean(view?.ok && src);

  return <div className="md-svg" data-svg-mode={previewable ? mode : "source"}>
    <div className="md-svg__toolbar">
      <div className="md-svg__title" aria-hidden="true">SVG</div>
      <div className="md-svg__actions">
        <button type="button" className={`md-svg__icon-btn${mode === "preview" ? " md-svg__icon-btn--active" : ""}`}
          disabled={!previewable} onClick={() => select("preview")} aria-label={t("chat.svgPreview")} title={t("chat.svgPreview")}><Play size={14} /></button>
        <button type="button" className={`md-svg__icon-btn${mode === "source" ? " md-svg__icon-btn--active" : ""}`}
          onClick={() => select("source")} aria-label={t("chat.svgSource")} title={t("chat.svgSource")}><Code2 size={14} /></button>
        <CopyButton getText={() => value} label={t("chat.svgCopy")} showInlineLabel={false} className="md-svg__copy" />
      </div>
    </div>
    {!previewable || mode === "source"
      ? <>
          <CodeViewer value={value} copyValue={value} language="svg" scrollMode="bounded" maxHeight={PREVIEW_MAX_HEIGHT} />
          {view && !view.ok && <p className="md-svg__note" role="status">{t("chat.svgPreviewBlocked")}</p>}
        </>
      : <div className="md-svg__preview" style={{ aspectRatio: ratio }}>
          <img src={src!} alt="SVG preview" referrerPolicy="no-referrer" style={{ maxHeight: PREVIEW_MAX_HEIGHT }} />
        </div>}
  </div>;
});

export default MarkdownSvgBlock;
