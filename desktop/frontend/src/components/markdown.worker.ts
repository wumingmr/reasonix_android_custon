// markdown.worker — off-main-thread Markdown parse (Phase E). Receives the
// raw answer text, runs the isomorphic pipeline (normalizeMath → remark →
// rehype → react-markdown transforms → block slicing), and posts back
// JSON-serializable HAST blocks. The worker never cancels in-flight work; the
// client drops stale responses by request id.

import { parseMarkdown } from "../lib/markdownPipeline";
import type { MarkdownParseResponse, MarkdownWorkerRequest } from "../lib/markdownWorkerProtocol";

const workerScope = globalThis as unknown as {
  onmessage: ((event: MessageEvent<MarkdownWorkerRequest>) => void) | null;
  postMessage: (response: MarkdownParseResponse) => void;
};

const documents = new Map<string, string>();

workerScope.onmessage = (event) => {
  const request = event.data;
  if (request.op === "release") {
    documents.delete(request.documentId);
    return;
  }
  let text = request.text;
  if (request.op === "open" || request.op === "replace" || request.op === "finalize") {
    documents.set(request.documentId, text);
  } else if (request.op === "append") {
    text = (documents.get(request.documentId) ?? "") + text;
    documents.set(request.documentId, text);
  }
  try {
    workerScope.postMessage({ id: request.id, result: parseMarkdown(text) });
  } catch (error) {
    workerScope.postMessage({
      id: request.id,
      error: error instanceof Error ? error.message : String(error),
    });
  }
};
