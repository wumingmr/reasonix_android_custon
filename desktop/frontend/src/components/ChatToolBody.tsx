import { useEffect, useRef, useState } from "react";
import { ErrorMessage } from "./ErrorMessage";
import type { ChatNode } from "../lib/chatViewSource";
import type { ChatContentLoader } from "../lib/chatContentLoader";
import { useT } from "../lib/i18n";
import { diffsFor, subjectOf } from "../lib/tools";
import { TerminalBlock } from "./harness-chat/TerminalBlock";
import { DiffBlock } from "./harness-chat/DiffBlock";
import { WebBlock } from "./harness-chat/WebBlock";
import { normalizeSearchSources } from "../lib/searchSourcesPresentation";
import { parseSearchSources, searchOutputMetadata } from "../lib/searchSources";
import toolCss from "./harness-chat/ToolRow.styles";
import { classifyTool, toolPresentation } from "../lib/chatToolPresentation";
import { boundedPayloadSections, utf8Prefix } from "../lib/toolPayloadPreview";
import "./harness-chat/TerminalBlock.css";
import "./harness-chat/DiffBlock.css";
import "./harness-chat/Pill.css";
import "./harness-chat/ToolBody.css";
import "./harness-chat/WebBlock.css";

export default function ChatToolBody({ item, loader }: { item: Extract<ChatNode, { kind: "tool" }>["item"]; loader: ChatContentLoader }) {
  const t = useT();
  const [loaded, setLoaded] = useState<{ item: typeof item; text: string }>();
  const [pendingItem, setPendingItem] = useState<typeof item>();
  const [errorItem, setErrorItem] = useState<typeof item>();
  const busy = pendingItem === item;
  const error = errorItem === item;
  const epoch = useRef(0);
  useEffect(() => { return () => { epoch.current++; }; }, [item]);
  const full = loaded && loaded.item.id === item.id && loaded.item.args === item.args &&
    loaded.item.output === item.output && loaded.item.error === item.error && loaded.item.dataArchived === item.dataArchived
    ? loaded.text : undefined;
  const preview = JSON.stringify({ args: item.args, output: item.output, error: item.error, diff: item.fileDiff });
  const limited = full === undefined && (loader.needsFullContent(item, "tool") || preview.length > 8000);
  const load = async () => {
    const ticket = ++epoch.current; setPendingItem(item); setErrorItem(undefined);
    try {
      const text = await loader.load(item, "tool");
      if (ticket !== epoch.current) throw new Error("Tool changed or closed");
      const payload: unknown = JSON.parse(text);
      if (!payload || typeof payload !== "object" || Array.isArray(payload)) throw new Error("Invalid tool content");
      for (const key of ["args", "output", "error"] as const) {
        if (key in payload && (payload as Record<string, unknown>)[key] != null && typeof (payload as Record<string, unknown>)[key] !== "string") throw new Error("Invalid tool content");
      }
      setLoaded({ item, text });
    } catch { if (ticket === epoch.current) setErrorItem(item); }
    finally { if (ticket === epoch.current) setPendingItem(undefined); }
  };
  let value: { args?: string; output?: string; error?: string } = item;
  if (full !== undefined) { try { value = JSON.parse(full); } catch { /* Keep the available preview. */ } }
  let args: Record<string, unknown> = {};
  try { args = JSON.parse(value.args || "{}") || {}; } catch { /* Streaming arguments. */ }
  const labels = { copy: t("msg.copy"), copied: t("msg.copied"), collapse: t("common.collapse"), collapseAria: t("common.collapse"),
    expand: (hidden: number) => t("chat.expandLines", { count: hidden }), expandAria: (hidden: number) => t("chat.expandLines", { count: hidden }) };
  const diffs = !limited ? diffsFor(item.name, value.args || "{}") : [];
  const kind = classifyTool(item);
  const presentation = toolPresentation(item);
  const search = kind === "search" && !limited ? normalizeSearchSources(item.searchSources ?? parseSearchSources(value.output || "")) : undefined;
  const searchMeta = searchOutputMetadata(value.output);
  return <>
    {value.error && (kind === "shell" || diffs.length > 0) && <p role="alert"><ErrorMessage error={value.error} /></p>}
    {!limited && kind === "shell" && (item.execution || item.status === "running" || item.status === "stopped") ? <TerminalBlock command={typeof args.command === "string" ? args.command : value.args || ""}
      output={value.output} running={presentation.state === "running"} exitCode={presentation.exitCode}
      presentation={{ state: presentation.dot, label: t(presentation.label) }}
      maxLines={200} className={toolCss.terminalBody}
      labels={{ ...labels, signal: signal => signal, exitCode: code => `${code}`, running: t("chat.running"), failed: t("chat.failed"), done: t("chat.done"), noOutput: t("chat.noOutput") }} />
      : search && item.status === "done" ? <WebBlock kind="search" answer={searchMeta.summary ?? item.searchSummary}
        sources={search.visible.map(source => ({ url: source.href, title: source.title }))} truncated={search.hiddenCount > 0}
        labels={{ noResults: t("sources.notProvided"), sourcesTruncated: t("sources.hidden", { n: search.hiddenCount }), http: "HTTP", contentTruncated: t("chat.loadFull") }} className={toolCss.webBody} />
      : diffs.length ? <DiffBlock diffs={diffs.map(diff => ({ path: subjectOf(item.name, value.args || "{}"), oldText: diff.original, newText: diff.modified }))}
        maxLines={200} labels={{ ...labels, files: count => t("chat.files", { count }) }} className={toolCss.diffBody} />
        : <div className={toolCss.ioCard}><ToolPayload text={full ?? preview} preview={limited} /></div>}
    {limited && <button className="btn" disabled={busy} onClick={() => void load()}>{t(error ? "chat.loadFailed" : busy ? "chat.loading" : "chat.loadFull")}</button>}
  </>;
}

export function ToolPayload({ text, preview }: { text: string; preview: boolean }) {
  const t = useT();
  let value: unknown;
  try { value = JSON.parse(text); } catch { return <pre>{preview ? utf8Prefix(text, 16 * 1024) : text}</pre>; }
  if (!value || typeof value !== "object" || Array.isArray(value)) return <pre>{preview ? utf8Prefix(text, 16 * 1024) : text}</pre>;
  const entries = preview ? boundedPayloadSections(value as Record<string, unknown>)
    : Object.entries(value).filter(([, content]) => content != null).map(([key, content]) => ({ key, body: typeof content === "string" ? content : JSON.stringify(content, null, 2) }));
  return <>{entries.map(({ key, body }) => {
    const label = key === "args" ? t("chat.tool.input") : key === "output" ? t("chat.tool.output") : key === "error" ? t("tool.error") : key;
    if (key === "error") return <section key={key} className={toolCss.ioSection}><ErrorMessage error={body} /></section>;
    return <section key={key} className={toolCss.ioSection}><span className={toolCss.ioLabel}>{label}</span><pre className={toolCss.ioText} data-error={key === "error" || undefined}>{body}</pre></section>;
  })}</>;
}
