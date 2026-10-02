import { RESOURCE_BUDGETS } from "./resourceBudgets";

export const TOOL_PREVIEW_MAX_BYTES = RESOURCE_BUDGETS.toolPreviewBytes;
export const TOOL_PREVIEW_MAX_BLOCKS = RESOURCE_BUDGETS.toolPreviewBlocks;

const encoder = new TextEncoder();

export function utf8Prefix(text: string, maxBytes: number): string {
  if (maxBytes <= 0) return "";
  if (encoder.encode(text).byteLength <= maxBytes) return text;
  let low = 0, high = text.length;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    if (encoder.encode(text.slice(0, middle)).byteLength <= maxBytes) low = middle;
    else high = middle - 1;
  }
  if (low > 0 && /[\uD800-\uDBFF]/.test(text[low - 1])) low -= 1;
  return text.slice(0, low);
}

export function boundedPayloadSections(value: Record<string, unknown>): Array<{ key: string; body: string }> {
  let remaining = TOOL_PREVIEW_MAX_BYTES;
  const sections: Array<{ key: string; body: string }> = [];
  for (const [key, content] of Object.entries(value).slice(0, TOOL_PREVIEW_MAX_BLOCKS)) {
    if (content == null || remaining <= 0) continue;
    const body = typeof content === "string" ? content : JSON.stringify(content, null, 2);
    const shown = utf8Prefix(body, remaining);
    remaining -= encoder.encode(shown).byteLength;
    sections.push({ key, body: shown });
  }
  return sections;
}
