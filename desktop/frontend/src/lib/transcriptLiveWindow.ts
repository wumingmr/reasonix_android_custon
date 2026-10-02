export interface TranscriptWindowPage {
  entryIds: string[];
  olderCursor: string;
  newerCursor: string;
  appendable?: boolean;
}

export function appendLivePageEntries(pages: TranscriptWindowPage[], entryIds: string[], pageSize: number): void {
  for (let offset = 0; offset < entryIds.length;) {
    const tail = pages[pages.length - 1];
    if (tail?.appendable && tail.entryIds.length < pageSize) {
      const count = Math.min(pageSize - tail.entryIds.length, entryIds.length - offset);
      tail.entryIds.push(...entryIds.slice(offset, offset + count));
      offset += count;
      continue;
    }
    const page = entryIds.slice(offset, offset + pageSize);
    pages.push({ entryIds: page, olderCursor: "", newerCursor: "", appendable: true });
    offset += page.length;
  }
}
