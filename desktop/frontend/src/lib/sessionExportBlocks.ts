import { unified } from "unified";
import remarkParse from "remark-parse";
import remarkGfm from "remark-gfm";
import remarkMath from "remark-math";

const blockBudget = 32 << 10;
function chunks(text: string): string[] {
  const out: string[] = [];
  while (text.length > blockBudget) {
    let end = text.lastIndexOf("\n", blockBudget);
    if (end < blockBudget / 2) end = text.lastIndexOf(" ", blockBudget);
    if (end < blockBudget / 2) end = blockBudget;
    else end++;
    const code = text.charCodeAt(end - 1); if (code >= 0xd800 && code <= 0xdbff) end--;
    out.push(text.slice(0, end)); text = text.slice(end);
  }
  if (text) out.push(text);
  return out;
}
/** Keep definitions available when a message's top-level blocks are paged. */
export function splitExportMarkdown(source: string): string[] {
  if (source.length <= blockBudget) return [source];
  const tree = unified().use(remarkParse).use(remarkGfm).use(remarkMath).parse(source);
  const definitions = tree.children.filter(node => node.type === "definition" || node.type === "footnoteDefinition")
    .map(node => source.slice(node.position?.start.offset, node.position?.end.offset)).join("\n");
  const out: string[] = [];
  for (const node of tree.children) {
    if (node.type === "definition" || node.type === "footnoteDefinition") continue;
    const text = source.slice(node.position?.start.offset, node.position?.end.offset);
    if (node.type === "code" && text.length > blockBudget) {
      const fence = "`".repeat(Math.max(2, ...Array.from(node.value.matchAll(/`+/g), match => match[0].length)) + 1);
      for (const part of chunks(node.value)) out.push(`${fence}${node.lang ?? ""}\n${part}\n${fence}`);
    } else if (node.type === "table" && text.length > blockBudget) {
      const lines = text.split("\n"); const header = lines.slice(0, 2).join("\n");
      let group = header;
      for (const line of lines.slice(2)) { if (group.length + line.length > blockBudget && group !== header) { out.push(group + "\n" + definitions); group = header; } group += "\n" + line; }
      out.push(group + "\n" + definitions);
    } else {
      for (const part of chunks(text)) out.push(part + (definitions ? "\n\n" + definitions : ""));
    }
  }
  return out;
}
