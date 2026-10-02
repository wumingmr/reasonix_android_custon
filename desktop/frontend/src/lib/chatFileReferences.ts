// Session-scoped owner for host-verified answer references.
//
// One store lives per mounted chat session. The transcript reports the paths a
// committed markdown block named; the store batches them, deduplicates against
// what it already asked, and calls the host once per batch. Results are cached
// by session, turn, and candidate, so a stream update or a remount re-renders
// from the cache instead of asking again.
//
// Everything that can outlive its session is generation-bound: switching the
// session, closing the tab, or changing the working directory disposes the
// store, and a late reply from an older generation is dropped rather than
// written into the replacement conversation.

import { app } from "./bridge";
import type { ChatFileReference, ChatFileReferenceRequest, ChatFileReferenceResult } from "../generated/desktopContract.generated";

/** Host verdict for one candidate. */
export interface ChatFileReferenceView {
  key: string;
  path: string;
  status: "resolved" | "unavailable" | "unsupported";
  displayPath?: string;
  kind?: string;
  actions: readonly string[];
  reason?: string;
}

/** Batches are capped by the host contract; the store splits larger sets. */
export const CHAT_FILE_REFERENCE_BATCH_LIMIT = 64;

type TurnState = {
  factsVersion: number;
  snapshot: ReadonlyMap<string, ChatFileReferenceView>;
  requested: Set<string>;
  queue: ChatFileReferenceRequest[];
  scheduled: boolean;
};

const emptyTurn: ReadonlyMap<string, ChatFileReferenceView> = new Map();

/** The wire type is a plain string; anything unrecognized is a refusal. */
function normalizeStatus(value: string): ChatFileReferenceView["status"] {
  return value === "resolved" || value === "unsupported" ? value : "unavailable";
}

export class ChatFileReferenceStore {
  private turns = new Map<string, TurnState>();
  private listeners = new Set<() => void>();
  private generation = 0;
  private disposed = false;
  private attachments = 0;

  constructor(private readonly tabId: string, private readonly hostId: string = "local") {}

  /** Only the local host can verify a path today; a remote host degrades to text. */
  get supported(): boolean { return this.hostId === "local"; }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  getTurnSnapshot = (turnKey: string): ReadonlyMap<string, ChatFileReferenceView> =>
    this.turns.get(turnKey)?.snapshot ?? emptyTurn;

  /**
   * Reports the candidates a committed block named. `factsVersion` identifies
   * the turn's file facts: when it advances, earlier failures are re-asked,
   * because a tool that just succeeded can turn a missing file into a real one.
   */
  report(turnKey: string, factsVersion: number, candidates: readonly ChatFileReferenceRequest[]): void {
    if (this.disposed || !this.supported || !candidates.length) return;
    const state = this.turnState(turnKey, factsVersion);
    if (state.factsVersion !== factsVersion) {
      state.factsVersion = factsVersion;
      for (const entry of state.snapshot.values()) {
        if (entry.status !== "resolved") state.requested.delete(entry.key);
      }
    }
    let queued = false;
    for (const candidate of candidates) {
      if (!candidate.key || state.requested.has(candidate.key)) continue;
      state.requested.add(candidate.key);
      state.queue.push(candidate);
      queued = true;
    }
    if (queued) this.schedule(turnKey, state);
  }

  /**
   * React StrictMode replays mount effects, running a cleanup without a
   * re-render. Disposal is therefore deferred and counted: a synchronous
   * re-attach revives the same store instead of killing it for the session.
   */
  attach(): void {
    this.attachments++;
  }

  detach(): void {
    this.attachments--;
    queueMicrotask(() => { if (this.attachments === 0) this.dispose(); });
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.generation++;
    this.turns.clear();
    this.listeners.clear();
  }

  private turnState(turnKey: string, factsVersion: number): TurnState {
    let state = this.turns.get(turnKey);
    if (!state) {
      state = { factsVersion, snapshot: new Map(), requested: new Set(), queue: [], scheduled: false };
      this.turns.set(turnKey, state);
    }
    return state;
  }

  /** One flush per microtask keeps a burst of committed blocks in one batch. */
  private schedule(turnKey: string, state: TurnState): void {
    if (state.scheduled) return;
    state.scheduled = true;
    queueMicrotask(() => {
      state.scheduled = false;
      if (!this.disposed) void this.flush(turnKey, state);
    });
  }

  private async flush(turnKey: string, state: TurnState): Promise<void> {
    while (state.queue.length) {
      const batch = state.queue.splice(0, CHAT_FILE_REFERENCE_BATCH_LIMIT);
      const generation = this.generation;
      let verdicts: readonly ChatFileReference[];
      try {
        verdicts = (await this.resolve(turnKey, batch)).references;
      } catch {
        verdicts = batch.map(item => ({ key: item.key, path: item.path, status: "unsupported", actions: [], reason: "unavailable-host" }));
      }
      // A reply from a replaced session must never reach the new one.
      if (this.disposed || generation !== this.generation) return;
      this.apply(state, verdicts);
    }
  }

  private resolve(turnKey: string, batch: ChatFileReferenceRequest[]): Promise<ChatFileReferenceResult> {
    return app.ResolveChatFileReferencesForTab(this.tabId, turnKey, batch);
  }

  private apply(state: TurnState, references: readonly ChatFileReference[]): void {
    if (!references.length) return;
    const next = new Map(state.snapshot);
    for (const reference of references) {
      const previous = next.get(reference.path);
      const status = normalizeStatus(reference.status);
      // A resolved verdict outlives an earlier failure for the same file.
      if (previous?.status === "resolved" && status !== "resolved") continue;
      const value: ChatFileReferenceView = {
        key: reference.key,
        path: reference.path,
        status,
        displayPath: reference.displayPath,
        kind: reference.kind,
        actions: reference.actions ?? [],
        reason: reference.reason,
      };
      next.set(reference.path, value);
      if (status === "resolved" && value.displayPath) next.set(value.displayPath, value);
    }
    state.snapshot = next;
    this.publish();
  }

  private publish(): void {
    for (const listener of this.listeners) listener();
  }
}
