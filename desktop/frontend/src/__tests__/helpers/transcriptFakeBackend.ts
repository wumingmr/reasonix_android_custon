import type { HistoryContentChunk, HistoryContentRef, HistoryEntry, HistoryMessage, HistorySlice, HistorySliceRequest } from "../../lib/types";
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

export type RefTable = Map<string, string>; // `${entryId}:${field}` -> full content

export class FakeBackend {
  sliceCalls: HistorySliceRequest[] = [];
  contentCalls: Array<{ ref: HistoryContentRef; chunk: number }> = [];
  sliceGate: ReturnType<typeof deferred<HistorySlice>> | undefined;
  contentGate: ReturnType<typeof deferred<HistoryContentChunk>> | undefined;
  staleNextCursor = false;
  revision = 1;
  digest = "digest-1";

  constructor(
    private readonly messages: HistoryMessage[],
    private readonly refs: RefTable = new Map(),
    private readonly sessionId = "s1",
  ) {}

  private entryId(index: number): string {
    return `${this.sessionId}:r0:m${index}:o0`;
  }

  private entriesFor(lo: number, hi: number): HistoryEntry[] {
    let turn = 0;
    const turnsOf: number[] = [];
    for (const message of this.messages) {
      if (message.role === "user") turn += 1;
      turnsOf.push(turn);
    }
    return this.messages.slice(lo, hi).map((message, offset) => {
      const index = lo + offset;
      const entryId = this.entryId(index);
      const refs: HistoryContentRef[] = [];
      let msg = message;
      const full = this.refs.get(`${entryId}:content`);
      if (full !== undefined) {
        msg = { ...message, content: full.slice(0, 16) };
        refs.push({ entryId, field: "content", size: full.length, chunks: 2, revision: 1, digest: "d" });
      }
      return { entryId, turn: turnsOf[index], order: index, message: msg, refs };
    });
  }

  slice(lo: number, hi: number): HistorySlice {
    const entries = this.entriesFor(lo, hi);
    const turns = entries.map((entry) => entry.turn).filter((value) => value > 0);
    return {
      entries,
      nextCursor: lo > 0 ? btoa(JSON.stringify({ v: 1, before: lo })) : "",
      hasOlder: lo > 0,
      newerCursor: hi < this.messages.length ? btoa(JSON.stringify({ v: 1, after: hi })) : "",
      hasNewer: hi < this.messages.length,
      totalTurns: this.messages.filter((message) => message.role === "user").length,
      startTurn: turns.length > 0 ? Math.min(...turns) : 0,
      endTurn: turns.length > 0 ? Math.max(...turns) : 0,
      stale: false,
      revision: this.revision,
      revisionKnown: true,
      digest: this.digest,
    };
  }

  // Turn- and entry-budgeted windowing, mirroring the Go slice semantics for
  // the compact stress fixtures used below.
  async HistorySliceForTab(_tabID: string, req: HistorySliceRequest): Promise<HistorySlice> {
    this.sliceCalls.push(req);
    if (this.sliceGate) {
      const gate = this.sliceGate;
      this.sliceGate = undefined;
      return gate.promise;
    }
    if (req.newer) {
      // Window paging toward newer history: `after` is an exclusive position.
      const decoded = JSON.parse(atob(req.cursor)) as { after?: number };
      const lo = Math.min(this.messages.length, decoded.after ?? this.messages.length);
      const entries = Math.max(1, Math.floor(req.entries || 120));
      return this.slice(lo, Math.min(this.messages.length, lo + entries));
    }
    let before = this.messages.length;
    if (req.cursor) {
      if (this.staleNextCursor) return { entries: [], nextCursor: "", hasOlder: false, totalTurns: 0, startTurn: 0, endTurn: 0, stale: true, revision: this.revision, revisionKnown: true, digest: this.digest };
      const decoded = JSON.parse(atob(req.cursor)) as { before?: number };
      before = Math.min(before, decoded.before ?? before);
    }
    if (before <= 0 || this.messages.length === 0) return this.slice(0, 0);
    let turn = 0;
    const turnsOf: number[] = [];
    for (const message of this.messages) {
      if (message.role === "user") turn += 1;
      turnsOf.push(turn);
    }
    const turns = Math.max(1, Math.floor(req.turns || 12));
    const newestTurn = turnsOf[before - 1];
    const oldestTurn = newestTurn > 0 ? Math.max(newestTurn - turns + 1, 1) : 0;
    let lo = 0;
    if (oldestTurn > 1) {
      lo = before;
      for (let i = 0; i < before; i += 1) {
        if (turnsOf[i] >= oldestTurn) { lo = i; break; }
      }
    }
    const entries = Math.max(1, Math.floor(req.entries || 120));
    lo = Math.max(lo, before - entries);
    return this.slice(lo, before);
  }

  async HistoryContentForTab(_tabID: string, ref: HistoryContentRef, chunkIndex: number): Promise<HistoryContentChunk> {
    this.contentCalls.push({ ref, chunk: chunkIndex });
    if (this.contentGate) {
      const gate = this.contentGate;
      this.contentGate = undefined;
      return gate.promise;
    }
    const full = this.refs.get(`${ref.entryId}:${ref.field}`) ?? "";
    const half = Math.ceil(full.length / 2);
    const data = chunkIndex === 0 ? full.slice(0, half) : full.slice(half);
    return { entryId: ref.entryId, field: ref.field, chunk: chunkIndex, chunks: 2, data, done: chunkIndex >= 1, stale: false };
  }
}
