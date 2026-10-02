import { lazy, Suspense, type CSSProperties } from "react";
import { CopyButton } from "./CopyButton";
import { useT } from "../lib/i18n";

export type CodeScrollMode = "expand" | "bounded";

export interface EditorProps {
  value: string;
  /** Complete source when the displayed code is a folded preview. */
  copyValue?: string;
  language?: string;
  readOnly?: boolean;
  scrollMode?: CodeScrollMode;
  maxHeight?: CSSProperties["maxHeight"];
  /** Original source size in bytes when the caller already has it. */
  sourceSize?: number;
  /** Opt in to the workspace-oriented viewer with line numbers and search. */
  showLineNumbers?: boolean;
  /** Markdown fences keep language and copy controls outside the scrolling source. */
  showHeader?: boolean;
  /** Request that the workspace-oriented viewer opens search after mounting. */
  searchRequestPending?: boolean;
  /** Called once the viewer has consumed a pending search request. */
  onSearchRequestConsumed?: () => void;
}

// ── EDITOR SEAM (code) ───────────────────────────────────────────────────────
// Keep the established highlighted viewer for existing chat, diff, and tool
// surfaces. Workspace previews explicitly opt into the heavier searchable
// viewer, so this feature cannot silently change every code block in the app.
const HljsImpl = lazy(() => Promise.all([import("./editors/HljsCode"), import("./CodeSyntax.css")]).then(([module]) => module));
const LineNumberImpl = lazy(() => Promise.all([import("./editors/LineNumberCode"), import("./CodeSyntax.css")]).then(([module]) => module));

function CodeBlockHeader({ value, copyValue, language }: EditorProps) {
  const t = useT();
  return <div className="code-block__header">
    <span className="code-block__language" title={language}>{language || t("code.plainText")}</span>
    <CopyButton text={copyValue ?? value} className="code-block__header-copy" />
  </div>;
}

export function CodeViewer(props: EditorProps) {
  const Impl = props.showLineNumbers ? LineNumberImpl : HljsImpl;
  const framed = props.showHeader && !props.showLineNumbers;
  const bounded = props.scrollMode === "bounded" || (props.scrollMode !== "expand" && props.maxHeight != null);
  return (
    <div className={`code-block${framed ? " code-block--framed" : ""}`}>
      {framed && <CodeBlockHeader {...props} />}
      <Suspense
        fallback={
          <pre className={`code code--loading${bounded ? " code--scroll-y" : ""}`} data-nested-scroll={bounded ? "" : undefined}>
            <code>{props.value}</code>
          </pre>
        }
      >
        <Impl {...props} />
      </Suspense>
    </div>
  );
}
