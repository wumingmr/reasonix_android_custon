// A highlight.js span can cross newlines. Balance it at each line boundary so
// React can retain unchanged line hosts without losing multiline token styles.
export function splitHighlightedCodeLines(html: string): string[] {
  const lines: string[] = [];
  const openTags: string[] = [];
  let current = "";
  let offset = 0;
  const closeTags = () => [...openTags].reverse()
    .map(tag => tag.startsWith("<mark") ? "</mark>" : "</span>").join("");
  while (offset < html.length) {
    if (html[offset] === "\n") {
      lines.push(current + closeTags());
      current = openTags.join("");
      offset += 1;
      continue;
    }
    if (html[offset] === "<") {
      const end = html.indexOf(">", offset);
      if (end !== -1) {
        const tag = html.slice(offset, end + 1);
        current += tag;
        if (/^<(span|mark)\b/.test(tag)) openTags.push(tag);
        else if (/^<\/(span|mark)>$/.test(tag)) openTags.pop();
        offset = end + 1;
        continue;
      }
    }
    const length = (html.codePointAt(offset) ?? 0) > 0xffff ? 2 : 1;
    current += html.slice(offset, offset + length);
    offset += length;
  }
  lines.push(current + closeTags());
  return lines;
}
