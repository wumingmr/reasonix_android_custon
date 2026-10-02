import type { MarkdownBlock } from "./markdownPipeline";
import { RESOURCE_BUDGETS } from "./resourceBudgets";

export const MARKDOWN_AST_ELEMENT_PAGE = RESOURCE_BUDGETS.markdownAstElementsPerPage;

/** Publish whole top-level blocks until the page budget is reached. */
export function visibleMarkdownBlockCount(blocks: readonly MarkdownBlock[], elementBudget: number): number {
  let elements = 0;
  for (let index = 0; index < blocks.length; index += 1) {
    const next = Math.max(1, blocks[index]?.elementCount ?? 1);
    // A single semantic block is indivisible and remains visible even when it
    // exceeds the target. Tables have their own nested cell-page budget.
    if (index > 0 && elements + next > elementBudget) return index;
    elements += next;
  }
  return blocks.length;
}
