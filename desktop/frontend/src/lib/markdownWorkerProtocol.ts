import type { MarkdownParseResult } from "./markdownPipeline";

export type MarkdownWorkerPriority = "interactive" | "visible" | "background";

export interface MarkdownParseRequest {
  id: number;
  op?: "parse";
  text: string;
}

export type MarkdownDocumentRequest =
  | { id: number; op: "open"; documentId: string; text: string }
  | { id: number; op: "append"; documentId: string; text: string }
  | { id: number; op: "replace"; documentId: string; text: string }
  | { id: number; op: "finalize"; documentId: string; text: string }
  | { id: number; op: "release"; documentId: string };

export type MarkdownWorkerRequest = MarkdownParseRequest | MarkdownDocumentRequest;

export interface MarkdownParseResponse {
  id: number;
  result?: MarkdownParseResult;
  error?: string;
}

export function markdownPriorityRank(priority: MarkdownWorkerPriority): number {
  if (priority === "interactive") return 0;
  if (priority === "visible") return 1;
  return 2;
}
