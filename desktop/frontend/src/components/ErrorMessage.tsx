import { useId, useState } from "react";
import { useI18n } from "../lib/i18n";
import { presentError } from "../lib/errorPresentation";
import type { WireEvent } from "../lib/types";

/** Inline-safe error surface, including inside paragraphs and existing banners. */
export function ErrorMessage({ error, summary, diagnostic, onInspect }: { error: unknown; summary?: string; diagnostic?: WireEvent["diagnostic"]; onInspect?: () => void }) {
  const { t, locale } = useI18n();
  const id = useId();
  const [expandedError, setExpandedError] = useState<{ error: unknown } | null>(null);
  const presentation = presentError(error, t, locale, diagnostic);
  const expanded = expandedError !== null && expandedError.error === error;
  const title = summary || presentation.summary;
  const detail = error == null || error === "" ? "" : presentation.detail || (summary ? presentation.summary : "");
  if ((error == null || error === "") && !summary) return null;
  return <span className="user-error">
    <span className="user-error__summary">{title}</span>
    {detail && detail !== title && <>
      <button type="button" className="user-error__toggle" aria-expanded={expanded} aria-controls={id}
        onClick={(event) => { event.stopPropagation(); onInspect?.(); setExpandedError(expanded ? null : { error }); }}>
        {t(expanded ? "error.hideDetails" : "error.details")}
      </button>
      {expanded && <span id={id} className="user-error__detail" onClick={event => event.stopPropagation()}>{detail}</span>}
    </>}
  </span>;
}
