import { memo, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { hastBlockToJsx } from "../lib/hastJsx";
import { estimateHastBytes, markdownContentRevision, type MarkdownBlock, type MarkdownParseResult } from "../lib/markdownPipeline";
import { getMarkdownWorkerClient } from "../lib/markdownWorkerClient";
import { getTranscriptStore } from "../lib/transcriptStore";
import { MARKDOWN_AST_ELEMENT_PAGE, visibleMarkdownBlockCount } from "../lib/markdownDomBudget";
import { useT } from "../lib/i18n";
import { createComponents } from "./markdownComponents";
import { useChatFileCandidateReport } from "./ChatFileLinkContext";
import { MarkdownSourceTable } from "./MarkdownTable";
import "katex/dist/katex.min.css";
import "./harness-chat/MarkdownText.css";

const Block = memo(function Block({ block, components }: { block: MarkdownBlock; components: ReturnType<typeof createComponents> }) {
  return block.virtualTable ? <MarkdownSourceTable data={block.virtualTable} /> : hastBlockToJsx(block, components);
});

let nextWorkerDocumentId = 1;

/**
 * Worker parsing is independent of viewport geometry. Every block uses natural flow.
 * cacheKey opts a row into the shared parse cache, which is addressed by content.
 */
const MarkdownHistory = memo(function MarkdownHistory({ text, streaming = false, plainStatusBlocks = false, cacheKey, fallback, onParsed, onError }: {
  text: string; streaming?: boolean; plainStatusBlocks?: boolean; cacheKey?: string; fallback: ReactNode;
  onParsed?: () => void; onError?: () => void;
}) {
  const t = useT();
  // The revision is a cache key for settled content only: a streaming body is
  // never stored, so hashing it on every delta would be O(body) work per
  // commit for a lookup that cannot hit.
  const revision = useMemo(() => (streaming ? 0 : markdownContentRevision(text)), [streaming, text]);
  const [parsed, setParsed] = useState<{ text: string; result: MarkdownParseResult }>();
  const previous = useRef<MarkdownParseResult | undefined>(undefined);
  const [workerDocumentId] = useState(() => `markdown-${nextWorkerDocumentId++}`);
  const root = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(typeof IntersectionObserver === "undefined");
  const [elementBudget, setElementBudget] = useState<number>(MARKDOWN_AST_ELEMENT_PAGE);
  useEffect(() => {
    if (visible || typeof IntersectionObserver === "undefined" || !root.current) return;
    const observer = new IntersectionObserver(entries => {
      if (entries.some(entry => entry.isIntersecting)) { setVisible(true); observer.disconnect(); }
    }, { rootMargin: "800px" });
    observer.observe(root.current);
    return () => observer.disconnect();
  }, [visible]);
  const components = useMemo(() => createComponents(plainStatusBlocks), [plainStatusBlocks]);
  const cached = useMemo(
    () => (!streaming && cacheKey ? getTranscriptStore().getMarkdown(text, revision) : undefined),
    [cacheKey, revision, streaming, text],
  );
  useEffect(() => {
    const client = getMarkdownWorkerClient();
    return () => client.releaseDocument(workerDocumentId);
  }, [workerDocumentId]);
  useEffect(() => {
    if (!visible && !streaming) return;
    if (cached?.blocks) { onParsed?.(); return; }
    let cancelled = false;
    const request = getMarkdownWorkerClient().parseDocument(workerDocumentId, text, {
      final: !streaming,
      priority: streaming ? "interactive" : visible ? "visible" : "background",
    });
    void request.promise.then(result => {
      if (cancelled || !result) return;
      // Retain unchanged AST identities across stream publications and
      // finalization, so React keeps native selection and code disclosure
      // hosts. The comparison is the fingerprint the parse already computed:
      // serializing both trees here cost O(blocks x block size) of string
      // allocation on the main thread on every streamed commit.
      const stable = previous.current?.blocks;
      if (stable) result.blocks = result.blocks.map((block, index) =>
        stable[index]?.key === block.key && stable[index]?.fingerprint === block.fingerprint ? stable[index] : block);
      previous.current = result;
      setParsed({ text, result });
      if (cacheKey && !streaming) getTranscriptStore().setMarkdown(revision, {
        source: text, blocks: result.blocks, selectionText: result.selectionText,
        selectionRevision: result.selectionRevision,
        bytes: text.length * 2 + result.selectionText.length * 2 + estimateHastBytes(result.blocks),
      });
      onParsed?.();
    }).catch(() => { if (!cancelled) onError?.(); });
    return () => { cancelled = true; request.cancel(); };
  }, [cacheKey, cached, onError, onParsed, revision, streaming, text, visible, workerDocumentId]);
  const result = cached?.blocks ? cached : parsed && (parsed.text === text || text.startsWith(parsed.text)) ? parsed.result : undefined;
  // Only blocks the parser has already committed are reported, so a streaming
  // answer never asks the host about a half-written path.
  useChatFileCandidateReport(result?.blocks, revision);
  const visibleBlockCount = result ? visibleMarkdownBlockCount(result.blocks, elementBudget) : 0;
  const nodes = useMemo(() => result?.blocks.slice(0, visibleBlockCount)
    .map(block => <Block key={block.key} block={block} components={components} />), [result, components, visibleBlockCount]);
  const pending = !cached?.blocks && parsed && text.startsWith(parsed.text) ? text.slice(parsed.text.length) : "";
  const hiddenBlocks = (result?.blocks.length ?? 0) - visibleBlockCount;
  return <div ref={root} className="md" data-markdown-blocks={result?.blocks.length}
    data-markdown-visible-blocks={visibleBlockCount}>
    {result ? <>{nodes}{hiddenBlocks > 0 && <button type="button" className="btn"
      onClick={() => setElementBudget(value => value + MARKDOWN_AST_ELEMENT_PAGE)}>
      {t("chat.loadMoreBlocks", { count: hiddenBlocks })}
    </button>}{!hiddenBlocks && pending && <span style={{ whiteSpace: "pre-wrap" }}>{pending}</span>}</> : fallback}
  </div>;
});
export default MarkdownHistory;
