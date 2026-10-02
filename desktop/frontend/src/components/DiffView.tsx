import { lazy, Suspense } from "react";

export interface DiffProps {
  original?: string;
  modified?: string;
  diff?: string;
  language?: string;
  maxHeight?: number;
}

// ── EDITOR SEAM (diff) ───────────────────────────────────────────────────────
// before/after rendering for edit tools, mirroring CodeViewer's seam. Swap the
// lazily-imported module to upgrade:
//
//   ./editors/HljsDiff         current — highlight.js line diff (LCS)
//   ./editors/MonacoDiff       monaco DiffEditor via @monaco-editor/react
//   ./editors/CodeMirrorMerge  @codemirror/merge
//
// The replacement only has to honor DiffProps. See desktop/README.md.
const Impl = lazy(() => Promise.all([import("./editors/HljsDiff"), import("./CodeSyntax.css")]).then(([module]) => module));

export function DiffView(props: DiffProps) {
  return (
    <Suspense fallback={<pre className="code code--loading">{props.modified ?? props.diff ?? ""}</pre>}>
      <Impl {...props} />
    </Suspense>
  );
}
