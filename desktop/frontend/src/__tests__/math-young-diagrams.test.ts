// Golden cases for the Young diagram / tableau macros in the math pipeline.
//
// Run: tsx src/__tests__/math-young-diagrams.test.ts

import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import ReactMarkdown from "react-markdown";
import { reasonixRehypePlugins, reasonixRemarkPlugins } from "../components/markdownRemarkPlugins";
import { normalizeMath, resolveProtectedInlineMathSource } from "../components/mathNormalize";
import { expandYoungDiagrams } from "../components/youngDiagrams";

let passed = 0;
let failed = 0;

function check(label: string, fn: () => boolean) {
  try {
    if (fn()) { process.stdout.write(`  PASS  ${label}
`); passed += 1; }
    else      { process.stdout.write(`  FAIL  ${label}
`); failed += 1; }
  } catch (e) {
    process.stdout.write(`  ERROR ${label}: ${(e as Error).message}
`); failed += 1;
  }
}

function renderHtml(src: string): string {
  return renderToStaticMarkup(
    createElement(ReactMarkdown, {
      remarkPlugins: reasonixRemarkPlugins,
      rehypePlugins: reasonixRehypePlugins,
      children: normalizeMath(src),
    }),
  );
}

// ── Young diagram / tableau macros ─────────────────────────────────────────────
// `\yng` (ytableau) and `\young` (youngtab) are common in physics —
// SU(N) irreps, tensor decompositions, character tables — but KaTeX
// doesn't bundle either macro package. The pre-pass translates them
// to KaTeX-compatible `\boxed{array}` forms inside the math body so
// the diagram renders as a grid of boxes.

console.log("\nnormalizeMath — Young diagram macros");

check("\\yng(2,1) renders as (2,1) Young diagram", () => {
  const html = renderHtml("$$\\yng(2,1)$$");
  return html.includes("katex-display") && !html.includes("katex-error");
});
check("\\yng(2,1) in prose (no $ delimiters) gets wrapped and rendered", () => {
  // A model that writes "the partition \\yng(2,1) corresponds to the
  // (2,1) irrep" doesn't usually put $$ around the macro. The
  // translator wraps bare \\yng in `$…$` so remark-math sees it as
  // inline math and katex renders the diagram.
  const html = renderHtml("The partition \\yng(2,1) is symmetric.");
  return html.includes("katex")
    && !html.includes("katex-error")
    && !html.includes("reasonixInternal")
    && html.includes('data-latex-source="\\yng(2,1)"');
});
check("\\yng inside \\(...\\) does not get double-wrapped", () => {
  const out = resolveProtectedInlineMathSource(normalizeMath("\\(\\yng(2,1)\\)"));
  return out.source === "$\\yng(2,1)$"
    && out.rendered === "$\\begin{array}{l}\\square \\! \\square \\\\[-0.525em] \\square\\end{array}$";
});
check("\\yng inside \\[...\\] stays display math without triple dollars", () => {
  const out = resolveProtectedInlineMathSource(normalizeMath("\\[\\yng(2,1)\\]"));
  return out.source === "$$\n\\yng(2,1)\n$$"
    && out.rendered.startsWith("$$\n\\begin{array}{l}")
    && out.rendered.endsWith("$$")
    && !out.rendered.includes("$$$");
});
check("escaped dollar before bare \\yng does not suppress wrapping", () => {
  const src = String.raw`Price is \$5; shape \yng(2,1)`;
  const expected = String.raw`Price is \$5; shape $\begin{array}{l}\square \! \square \\[-0.525em] \square\end{array}$`;
  const out = resolveProtectedInlineMathSource(normalizeMath(src));
  return out.source === String.raw`Price is \$5; shape $\yng(2,1)$`
    && out.rendered === expected;
});
check("digit-starting inline math with \\yng does not get nested wrappers", () => {
  const out = resolveProtectedInlineMathSource(normalizeMath("$3\\,\\yng(2,1)$"));
  return out.source === "$3\\,\\yng(2,1)$"
    && out.rendered === "$3\\,\\begin{array}{l}\\square \\! \\square \\\\[-0.525em] \\square\\end{array}$";
});
check("digit-starting inline math with \\young does not get nested wrappers", () => {
  const out = resolveProtectedInlineMathSource(normalizeMath("$2 + \\young(ab,c)$"));
  return out.source === "$2 + \\young(ab,c)$"
    && out.rendered === "$2 + \\begin{array}{l}\\boxed{a} \\! \\boxed{b} \\\\[-0.525em] \\boxed{c}\\end{array}$";
});
check("display math ending in digit closes before following bare \\yng", () => {
  const out = resolveProtectedInlineMathSource(normalizeMath("$$x^2$$ \\yng(1)"));
  return out.source === "$$\nx^2\n$$\n $\\yng(1)$"
    && out.rendered === "$$\nx^2\n$$\n $\\begin{array}{l}\\square\\end{array}$";
});
check("bare \\yng after inline math is separated from adjacent dollars", () => {
  const out = resolveProtectedInlineMathSource(normalizeMath("$x$\\yng(1)"));
  return out.source === "$x$ $\\yng(1)$"
    && out.rendered === "$x$ $\\begin{array}{l}\\square\\end{array}$";
});
check("bare \\yng before inline math is separated from adjacent dollars", () => {
  const out = resolveProtectedInlineMathSource(normalizeMath("\\yng(1)$x$"));
  return out.source === "$\\yng(1)$ $x$"
    && out.rendered === "$\\begin{array}{l}\\square\\end{array}$ $x$";
});
check("\\yng (2,1) with a space before parens gets wrapped and rendered", () => {
  const html = renderHtml("The partition \\yng (2,1) is symmetric.");
  return html.includes("katex")
    && !html.includes("katex-error")
    && !html.includes("reasonixInternal")
    && html.includes('data-latex-source="\\yng (2,1)"');
});
check("\\yng(3,2,1) renders as (3,2,1) Young diagram", () => {
  const html = renderHtml("$$\\yng(3,2,1)$$");
  return html.includes("katex-display") && !html.includes("katex-error");
});
check("\\yng(2,1){a&b\\\\c\\\\d&e} renders filled Young tableau", () => {
  const html = renderHtml("$$\\yng(2,1){a&b\\\\c\\\\d&e}$$");
  return html.includes("katex-display") && !html.includes("katex-error");
});
check("\\young(2 1) compatibility shorthand renders as (2,1) diagram", () => {
  const html = renderHtml("$$\\young(2 1)$$");
  return html.includes("katex-display") && !html.includes("katex-error");
});
check("\\young(ab,c) labelled youngtab syntax renders labels", () => {
  const html = renderHtml("$$\\young(ab,c)$$");
  return html.includes("katex-display")
    && !html.includes("katex-error")
    && !html.includes("reasonixInternal")
    && html.includes('data-latex-source="\\young(ab,c)"')
    && ["a", "b", "c"].every((label) => html.includes(label));
});
check("\\young(ab,c) labelled cells keep boxes", () => {
  const out = expandYoungDiagrams("\\young(ab,c)");
  return out.includes("\\boxed{a}")
    && out.includes("\\boxed{b}")
    && out.includes("\\boxed{c}");
});
check("\\young(abcd,:cd,:c) skew placeholders are invisible offsets", () => {
  const out = expandYoungDiagrams("\\young(abcd,:cd,:c)");
  return out.includes("\\hphantom{\\boxed{x}}")
    && !out.includes("\\boxed{:}");
});
check("\\yng(4,3,2,1) renders as (4,3,2,1) Young diagram", () => {
  const html = renderHtml("$$\\yng(4,3,2,1)$$");
  return html.includes("katex-display") && !html.includes("katex-error");
});
check("\\yng(3,2,1) uses left-aligned array (rows start at same x)", () => {
  // A Young diagram's shorter rows must start at the same x-position
  // as the longest row's first cell — `{l}` (left) instead of `{c}`
  // (centered) gives that layout. Without this, the diagram looks
  // like each row is independently centred, which isn't a Young
  // diagram.
  const out = expandYoungDiagrams("\\yng(3,2,1)");
  return out.includes("\\begin{array}{l}")
    && !out.includes("\\begin{array}{c}");
});
check("expandYoungDiagrams uses flush cells (\\! cancels \\,) ", () => {
  // Adjacent \square boxes should be flush — the convention for Young
  // diagrams. The translator uses `\!` (negative thin space, -0.1667em)
  // which exactly cancels `\,` so cells touch without visible gap.
  // `\,` (positive thin space) would leave a gap.
  const out = expandYoungDiagrams("\\yng(3)");
  return out.includes("\\!") && !out.includes("\\, ");
});
check("expandYoungDiagrams uses flush rows (\\[-0.525em] closes the math-axis gap)", () => {
  // The math axis positions a \square glyph centred on the row
  // baseline, which leaves a visible ~0.4em gap between the bottom of
  // one row's box and the top of the next row's box when the default
  // 1.2em baseline-to-baseline spacing is used. Using `\\[-0.4em]`
  // between rows pulls each subsequent row up by the math-axis offset,
  // so consecutive rows touch. (Earlier versions tried wrapping each
  // cell in `\raisebox{-0.35em}` which does NOT close the gap —
  // uniform translation can't change the relative distance between
  // row baselines.)
  const out = expandYoungDiagrams("\\yng(2,1)");
  return out.includes("\\\\[-0.525em]");
});
check("expandYoungDiagrams substitutes correct array form", () => {
  // Direct unit test on the translator — no need to go through the
  // full pipeline for this assertion.
  const out = expandYoungDiagrams("\\yng(2,1)");
  return out.includes("\\begin{array}{l}")
    && out.includes("\\square")
    && out.includes(" \\\\[-0.525em] ");
});
check("expandYoungDiagrams handles \\yng with content", () => {
  // Bare \yng in prose gets wrapped in `$…$` so remark-math sees it as
  // math; macros already inside a `$…$` block just substitute the inner
  // form (the surrounding delimiters are preserved).
  // Cells are joined with `\!` (negative thin space) so adjacent
  // boxes are flush. Row separators use `\\[-0.525em]` (per-row
  // negative spacing) so consecutive rows touch — the visible
  // glyph height of `\square` is 0.675em (measured from katex's
  // single-glyph strut), and the default 1.2em baseline spacing
  // leaves 0.525em of gap. `\\[-0.525em]` subtracts exactly that.
  const out = expandYoungDiagrams("\\yng(2,1){a&b\\\\c}");
  return out === "$\\begin{array}{l}\\boxed{a} \\! \\boxed{b} \\\\[-0.525em] \\boxed{c}\\end{array}$";
});
check("expandYoungDiagrams handles labelled \\young rows", () => {
  const out = expandYoungDiagrams("\\young(ab,c)");
  return out === "$\\begin{array}{l}\\boxed{a} \\! \\boxed{b} \\\\[-0.525em] \\boxed{c}\\end{array}$";
});
check("expandYoungDiagrams treats comma-separated numeric \\young as labels, not a 12-cell row", () => {
  const out = expandYoungDiagrams("\\young(12,3)");
  return out === "$\\begin{array}{l}\\boxed{1} \\! \\boxed{2} \\\\[-0.525em] \\boxed{3}\\end{array}$";
});
check("expandYoungDiagrams leaves invalid negative \\yng shape alone", () => {
  const out = expandYoungDiagrams("\\yng(-1)");
  return out === "\\yng(-1)";
});
check("expandYoungDiagrams leaves oversized \\yng shape alone", () => {
  const out = expandYoungDiagrams("\\yng(513)");
  return out === "\\yng(513)";
});
check("expandYoungDiagrams leaves non-Young macros alone", () => {
  const out = expandYoungDiagrams("\\frac{a}{b}");
  return out === "\\frac{a}{b}";
});

// ── Summary ───────────────────────────────────────────────────────────────────

console.log(`
${passed} passed, ${failed} failed, ${passed + failed} total`);
if (failed > 0) process.exit(1);
