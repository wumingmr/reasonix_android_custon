import type { MarkdownBlock } from "./markdownPipeline";

export interface ParsedMarkdownValue {
  source: string;
  blocks: MarkdownBlock[];
  selectionText: string;
  selectionRevision: number;
  /** Source/projection UTF-16 bytes plus the estimated HAST weight. */
  bytes: number;
}

/**
 * Byte-bounded LRU whose active selection entries may be pinned. Entries are
 * addressed by source content, not by row id: a parse is a pure function of
 * its text, and row ids differ between the live and history projections.
 */
export class TranscriptMarkdownCache {
  private readonly entries = new Map<number, { value: ParsedMarkdownValue; bytes: number }>();
  private readonly pins = new Map<number, number>();
  bytes = 0;
  evictions = 0;

  constructor(readonly budgetBytes: number) {}

  get(source: string, revision: number): ParsedMarkdownValue | undefined {
    const entry = this.entries.get(revision);
    if (!entry || entry.value.source !== source) return undefined;
    this.entries.delete(revision);
    this.entries.set(revision, entry);
    return entry.value;
  }

  set(revision: number, value: ParsedMarkdownValue): void {
    const previous = this.entries.get(revision);
    if (previous) this.bytes -= previous.bytes;
    const bytes = Math.max(0, value.bytes);
    this.entries.set(revision, { value, bytes });
    this.bytes += bytes;
    this.enforceBudget();
  }

  pin(revision: number): () => void {
    this.pins.set(revision, (this.pins.get(revision) ?? 0) + 1);
    let released = false;
    return () => {
      if (released) return;
      released = true;
      const count = this.pins.get(revision) ?? 0;
      if (count <= 1) this.pins.delete(revision);
      else this.pins.set(revision, count - 1);
      this.enforceBudget();
    };
  }

  size(): number {
    return this.entries.size;
  }

  private enforceBudget(): void {
    while (this.bytes > this.budgetBytes && this.entries.size > 1) {
      const victimKey = Array.from(this.entries.keys()).find((key) => !this.pins.has(key));
      if (victimKey === undefined) break;
      const victim = this.entries.get(victimKey);
      if (victim) this.bytes -= victim.bytes;
      this.entries.delete(victimKey);
      this.evictions += 1;
    }
  }
}
