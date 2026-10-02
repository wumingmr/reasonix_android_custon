import { Fragment, memo, useEffect, useMemo, useState } from "react";
import type { EditorProps } from "../CodeViewer";
import {
  escapeHtml,
  highlightToHtml,
  IDLE_HIGHLIGHT_MIN_BYTES,
  shouldHighlightSource,
  resolveLang,
} from "../../lib/highlight";
import { splitHighlightedCodeLines } from "../../lib/highlightLines";
import { scheduleIdleTask } from "../../lib/idleTask";
import { CopyButton } from "../CopyButton";

// HljsCode is the syntax-highlighted default behind the code editor seam. It
// renders highlight.js token markup into a <pre>; token colors live in CodeSyntax.css
// (.hljs-*). To upgrade to a full editor, point CodeViewer.tsx's lazy import at a
// Monaco/CodeMirror module honoring the same EditorProps.
//
// Large blocks (≥ IDLE_HIGHLIGHT_MIN_BYTES, still under the skip caps) mount
// the COMPLETE plain text immediately and swap in highlighted HTML from an
// idle callback; the skip caps (MAX_HIGHLIGHT_BYTES / MAX_HIGHLIGHT_LINES)
// remain the plain-forever policy. Either way nothing is ever truncated.
const CodeLine = memo(function CodeLine({ html }: { html: string }) {
  return <span className="code-block__line" dangerouslySetInnerHTML={{ __html: html }} />;
});

const HljsCode = memo(function HljsCode({ value, copyValue, language, scrollMode, maxHeight, sourceSize, showHeader }: EditorProps) {
  const syntaxHighlight = useMemo(
    () => Boolean(resolveLang(language)) && shouldHighlightSource(value, sourceSize),
    [language, sourceSize, value],
  );
  const deferToIdle = syntaxHighlight && (sourceSize ?? value.length) >= IDLE_HIGHLIGHT_MIN_BYTES;
  const [idleResult, setIdleResult] = useState<{ value: string; language?: string; html: string } | null>(null);

  useEffect(() => {
    if (!deferToIdle) return;
    let cancelled = false;
    const cancelIdle = scheduleIdleTask(() => {
      if (cancelled) return;
      const html = highlightToHtml(value, language);
      setIdleResult({ value, language, html });
    });
    return () => {
      cancelled = true;
      cancelIdle();
    };
  }, [deferToIdle, language, value]);

  const idleHtml = idleResult && idleResult.value === value && idleResult.language === language
    ? idleResult.html
    : null;
  const html = useMemo(() => {
    if (!deferToIdle) return highlightToHtml(value, syntaxHighlight ? language : undefined);
    // While an appended revision waits for idle, retain its already-highlighted
    // prefix. Replacements and language changes must never reuse stale markup.
    if (idleHtml === null && idleResult && idleResult.language === language && value.startsWith(idleResult.value)) {
      return idleResult.html + escapeHtml(value.slice(idleResult.value.length));
    }
    return idleHtml ?? escapeHtml(value);
  }, [deferToIdle, idleHtml, idleResult, language, syntaxHighlight, value]);
  const retainLines = showHeader && syntaxHighlight;
  const lines = useMemo(() => retainLines ? splitHighlightedCodeLines(html) : [], [html, retainLines]);
  const highlighted = syntaxHighlight && (!deferToIdle || idleHtml !== null);
  const bounded = scrollMode === "bounded" || (scrollMode !== "expand" && maxHeight != null);
  return (
    <div className="code-block__wrap">
      <pre
        className={`code hljs${bounded ? " code--scroll-y" : ""}`}
        data-nested-scroll={bounded ? "" : undefined}
        data-highlight-mode={highlighted ? "syntax" : "plain"}
        data-lang={language}
        style={maxHeight != null ? { maxHeight } : undefined}
      >
        {retainLines ? <code>{lines.map((line, index) => <Fragment key={index}>
          {index > 0 && "\n"}<CodeLine html={line} />
        </Fragment>)}</code> : <code dangerouslySetInnerHTML={{ __html: html }} />}
      </pre>
      {!showHeader && <CopyButton text={copyValue ?? value} className="code-block__copy" />}
    </div>
  );
});

export default HljsCode;
