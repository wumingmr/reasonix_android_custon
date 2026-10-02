// Candidates the answer body names but never declared with `present`.
//
// Extraction runs on the parsed blocks — inline code and local-path links —
// never on raw source, so a command, a regular expression, or the inside of a
// fenced block cannot become a file reference. Nothing here decides whether a
// file exists: every candidate is verified by the host before it becomes
// clickable, and a candidate that fails verification keeps its original text.

import type { Element, RootContent } from "hast";
import { fileIdentity, isAbsolutePath, pathBasename, pathExtension } from "./filePaths";
import { localPathFromHref } from "./localFileUrl";
import { unescapeRefPath } from "./refToken";

export interface ChatFileCandidate {
  /** Renderer identity; also the key the host echoes back. */
  key: string;
  /** The path exactly as the answer spelled it. */
  path: string;
}

/** A code span is only worth verifying when it carries a directory of its own. */
const DIRECTORY_SEPARATOR_RE = /[\\/]/;

function isCodeSpanCandidate(text: string): boolean {
  const value = unescapeRefPath(text.trim());
  if (!value || value.length > 4096 || value.includes("\n")) return false;
  if (isAbsolutePath(value)) return true;
  // A bare filename matches the turn's known file set instead, so it is never
  // sent to the host: resolving it would guess at a file the answer never named.
  return DIRECTORY_SEPARATOR_RE.test(value) && pathBasename(value) !== "";
}

/**
 * An explicit Markdown link may name a local file directly (`[x](/tmp/a.pdf)`)
 * as well as through a `file://` URL. Only an absolute path with a file
 * extension qualifies: a bare `/docs/getting-started` style site path must not
 * become a file reference.
 */
function hrefLocalPath(href: unknown): string | undefined {
  if (typeof href !== "string") return undefined;
  const decoded = localPathFromHref(href);
  if (decoded) return decoded;
  const trimmed = href.trim();
  if (!isAbsolutePath(trimmed) || !pathExtension(trimmed)) return undefined;
  return trimmed;
}

/**
 * Walks one parsed block. `code` elements are inline spans only — a fenced
 * block arrives as a `<pre><code>` pair and is skipped whole.
 */
function collectFromNode(node: RootContent, out: ChatFileCandidate[], seen: Set<string>): void {
  if (node.type === "element") {
    const element = node as Element;
    const tag = element.tagName.toLowerCase();
    if (tag === "pre") return;
    if (tag === "code") {
      const text = textOf(element).trim();
      if (text && isCodeSpanCandidate(text)) push(out, seen, unescapeRefPath(text));
      return;
    }
    if (tag === "a") {
      const decoded = hrefLocalPath(element.properties?.href);
      if (decoded) push(out, seen, decoded);
      // A link's own text is not scanned: it is a label, not a path.
      return;
    }
  }
  if ("children" in node && Array.isArray(node.children)) {
    for (const child of node.children) collectFromNode(child as RootContent, out, seen);
  }
}

function push(out: ChatFileCandidate[], seen: Set<string>, raw: string): void {
  const path = raw.trim();
  if (!path || path.length > 4096) return;
  const identity = fileIdentity(path);
  if (!identity || seen.has(identity)) return;
  seen.add(identity);
  out.push({ key: identity, path });
}

function textOf(element: Element): string {
  let text = "";
  for (const child of element.children ?? []) {
    if (child.type === "text") text += child.value;
    else if (child.type === "element") text += textOf(child as Element);
  }
  return text;
}

/** Deduplicated candidates for one parsed block list, in document order. */
export function chatFileCandidates(blocks: readonly { children: RootContent[] }[]): ChatFileCandidate[] {
  const out: ChatFileCandidate[] = [];
  const seen = new Set<string>();
  for (const block of blocks) {
    for (const child of block.children) collectFromNode(child, out, seen);
  }
  return out;
}
