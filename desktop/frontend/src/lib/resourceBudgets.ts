/**
 * Resource-governance defaults shared by transcript, parser and details.
 * User-reachable data stays behind paging; these values bound residency and
 * concurrency, not durable retention.
 */
export const RESOURCE_BUDGETS = Object.freeze({
  historyBodyBytes: 32 << 20,
  historyWindowPages: 3,
  historyPageEntries: 32,
  contentReadsGlobal: 4,
  contentReadsPerSession: 2,
  markdownAstElementsPerPage: 12_000,
  markdownTableCellsPerPage: 2_000,
  toolPreviewBytes: 16 * 1024,
  toolPreviewBlocks: 64,
  toolRelationsPerPage: 20,
});
