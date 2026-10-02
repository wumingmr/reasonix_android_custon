// markdownComponents — the shared components map for both Markdown render
// paths: react-markdown (streaming) and the worker-parsed block renderer
// (history). Kept in its own CSS-free module so the worker-path renderer and
// plain-tsx tests can import it without pulling the katex stylesheet.
//
// Fenced code blocks go through CodeViewer for syntax highlighting; inline
// code is a styled <code>. Mermaid fences lazy-load the diagram renderer.
// Links open in the system browser via RichMarkdownLink. Tables use natural
// document flow; large code fences have an explicit disclosure.

import { lazy, Suspense, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { Components } from "react-markdown";
import { CodeViewer } from "./CodeViewer";
import { RichMarkdownLink } from "./githubLink";
import { MarkdownTable } from "./MarkdownTable";
import { MarkdownImage } from "./MarkdownImage";
import { t } from "../lib/i18n";
import { useChatFileLink } from "./ChatFileLinkContext";
import { ChatFileReferenceAnchor, ChatFileReferenceCode } from "./ChatFileLink";
import { localPathFromHref } from "../lib/localFileUrl";
import { looksLikeSvgDocument } from "../lib/svgDocument";

const MermaidDiagram = lazy(() => import("./MermaidDiagram"));
const MarkdownSvgBlock = lazy(() => import("./MarkdownSvgBlock"));

/** Fences that may hold an SVG document; the body still has to prove it. */
const SVG_FENCES = new Set(["svg", "xml", "html"]);

export function MarkdownCode({ value, language }: { value: string; language?: string }) {
  const [expanded, setExpanded] = useState(false);
  const lines = useMemo(() => value.split("\n"), [value]);
  const large = lines.length > 200;
  return <><CodeViewer value={large && !expanded ? lines.slice(0, 200).join("\n") : value} copyValue={value} language={language} scrollMode="expand" showHeader />
    {large && <div className="chat-code-fold"><button className="btn" aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>{t(expanded ? "chat.collapseCode" : "chat.expandCode")}</button></div>}
  </>;
}

const STATUS_MARKER_RE = /(?:✅|☑|☒|✔️?|✓|\[[xX ]\])/;
const STATUS_MARKER_GLOBAL_RE = /(?:✅|☑|☒|✔️?|✓|\[[xX ]\])/g;
const BULLET_RE = /^[-*•]\s+\S/;
const DIVIDER_RE = /^[\s\-_=─━—]+$/;

function splitStatusLine(line: string): string[] {
  const parts = (line.match(STATUS_MARKER_GLOBAL_RE) ?? []).length > 1
    ? line.split(/(?=(?:✅|☑|☒|✔️?|✓|\[[xX ]\]))/)
    : [line];
  return parts
    .map((part) => part.replace(/^(?:✅|☑|☒|✔️?|✓|\[[xX ]\]|[-*•])\s*/i, "").trim())
    .filter(Boolean)
    .map((part) => part.replace(/\s{2,}/g, " · "));
}

function looksLikeDiagram(text: string): boolean {
  return /[←→↔]|<{1,2}-{2,}|-{2,}>{1,2}|[-_=─━]{6,}/.test(text);
}

function splitPlainBlock(text: string): { preText: string; statusItems: string[] } {
  const items: string[] = [];
  const preLines: string[] = [];
  const lines = text.split(/\r?\n/);
  const bulletLines = lines.filter((line) => BULLET_RE.test(line.trim())).length;
  const collectBulletLines = bulletLines >= 2 && !looksLikeDiagram(text);
  for (const rawLine of lines) {
    const line = rawLine.trim();
    const marked = STATUS_MARKER_RE.test(line) || (collectBulletLines && BULLET_RE.test(line));
    if (marked) {
      items.push(...splitStatusLine(line));
    } else if (DIVIDER_RE.test(line) && items.length > 0 && !looksLikeDiagram(text)) {
      continue;
    } else {
      preLines.push(rawLine);
    }
  }
  while (preLines.length > 0 && preLines[0].trim() === "") preLines.shift();
  while (preLines.length > 0 && preLines[preLines.length - 1].trim() === "") preLines.pop();
  return { preText: preLines.join("\n"), statusItems: items };
}

function PlainMarkdownBlock({ text }: { text: string }) {
  if (text.trim() === "") return null;
  const { preText, statusItems } = splitPlainBlock(text);
  const asList = statusItems.length >= 2;
  return (
    <div className={`md-plain-block${asList ? " md-plain-block--split" : " md-plain-block--pre"}`}>
      <CodeViewer value={text} scrollMode="bounded" maxHeight="min(60vh, 28rem)" />
      {asList && preText && (
        <div className="md-plain-block__diagram">
          <CodeViewer value={preText} scrollMode="bounded" maxHeight="min(60vh, 28rem)" />
        </div>
      )}
      {asList && (
        <div className="md-status-list">
          {statusItems.map((item, index) => (
            <div className="md-status-list__item" key={`${index}-${item}`}>
              <span className="md-status-list__dot" aria-hidden="true" />
              <span className="md-status-list__text">{item}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// The components map is shared by the main-thread react-markdown renderer
// (streaming path) and the worker-parsed block renderer (history path), so
// both produce byte-identical DOM for the same document.
export function createComponents(plainStatusBlocks: boolean): Components {
  return {
    pre: ({ children }) => <>{children}</>,
    table: ({ children }) => <MarkdownTable>{children}</MarkdownTable>,
    code: ({ className, children }) => {
      const text = String(children ?? "");
      const match = /language-([\w-]+)/.exec(className ?? "");
      const lang = match?.[1];
      const isBlock = match !== null || text.includes("\n");
      if (isBlock) {
        const value = text.replace(/\n$/, "");
        // An empty fence is a formatting placeholder; a bordered one-line
        // CodeViewer would otherwise become a phantom row in every surface.
        if (value.trim() === "") return null;
        if (lang === "mermaid") {
          return (
            <Suspense fallback={<CodeViewer value={value} language="mermaid" scrollMode="bounded" maxHeight="min(60vh, 28rem)" />}>
              <MermaidDiagram definition={value} />
            </Suspense>
          );
        }
        if (!match && plainStatusBlocks) return <PlainMarkdownBlock text={text.replace(/\n$/, "")} />;
        // An `svg` fence is always a candidate; `xml`, `html`, and a
        // language-less fence are candidates only when the body starts like a
        // single SVG document. The host's strict parse has the final word, and
        // anything it refuses renders as the ordinary code block.
        if (looksLikeSvgDocument(value) && (lang === undefined || SVG_FENCES.has(lang))) {
          return (
            <Suspense fallback={<MarkdownCode value={value} language={lang} />}>
              <MarkdownSvgBlock value={value} />
            </Suspense>
          );
        }
        return <MarkdownCode value={value} language={lang} />;
      }
      return <InlineMarkdownCode text={text}>{children}</InlineMarkdownCode>;
    },
    a: (props) => <MarkdownFileLink href={props.href} scanned={(props as Record<string, unknown>)["data-scanned-path"] !== undefined}>{props.children}</MarkdownFileLink>,
    img: ({ src, alt, title }) => <MarkdownImage src={src} alt={alt} title={title} />,
  };
}

/**
 * A local-path link is upgraded to a chat file reference when the host verified
 * it. A path that was only *scanned* out of prose stays ordinary text until it
 * is verified — a command, a URL path, or a directory that merely looks like a
 * file must not become a link. Every other link keeps its existing behavior.
 */
function MarkdownFileLink({ href, scanned, children }: { href?: string; scanned: boolean; children: ReactNode }) {
  const path = href ? localPathFromHref(href) : null;
  const link = useChatFileLink(path ?? "");
  const pendingOpen = useRef(false);
  useEffect(() => {
    if (!pendingOpen.current || !link) return;
    pendingOpen.current = false;
    void import("../lib/fileNavigationCommands")
      .then(({ openResource }) => openResource(link.ref, { view: "preview" }))
      .catch(() => undefined);
  }, [link]);
  if (path && link) return <ChatFileReferenceAnchor link={link} href={href!}>{children}</ChatFileReferenceAnchor>;
  if (path && scanned) return <span className="md-rich-link__plain">{children}</span>;
  if (path) {
    return <a href={href} aria-label={String(path)} onClick={(event) => {
      event.preventDefault();
      pendingOpen.current = true;
    }}>{children}</a>;
  }
  return <RichMarkdownLink href={href}>{children}</RichMarkdownLink>;
}

function InlineMarkdownCode({ text, children }: { text: string; children: ReactNode }) {
  const file = useChatFileLink(text);
  if (!file) return <code className="md-code">{children}</code>;
  return <ChatFileReferenceCode link={file}>{children}</ChatFileReferenceCode>;
}
