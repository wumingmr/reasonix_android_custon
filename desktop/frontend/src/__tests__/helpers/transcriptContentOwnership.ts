import assert from "node:assert/strict";
import { TranscriptStore } from "../../lib/transcriptStore";
import type { HistoryContentChunk, HistorySlice } from "../../lib/types";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(done => { resolve = done; });
  return { promise, resolve };
}

export async function verifyTranscriptContentOwnership() {
  const slice = (revision: number): HistorySlice => ({
    entries: [{ entryId: "m:answer", turn: 1, order: 0,
      message: { role: "assistant", content: `preview-${revision}` },
      refs: [{ entryId: "m:answer", field: "content", size: 100, chunks: 1, revision, digest: `cut-${revision}` }] }],
    revision, revisionKnown: true, digest: `cut-${revision}`, stale: false,
    nextCursor: "", hasOlder: false, totalTurns: 1, startTurn: 1, endTurn: 1,
  });
  const chunk = (data: string): HistoryContentChunk => ({
    entryId: "m:answer", field: "content", chunk: 0, chunks: 1, data, done: true, stale: false,
  });
  function fixture() {
    const requests: { revision: number; gate: ReturnType<typeof deferred<HistoryContentChunk>> }[] = [];
    const started = [deferred<void>(), deferred<void>()];
    const page = deferred<HistorySlice>();
    const store = new TranscriptStore({
      HistorySliceForTab: () => page.promise,
      HistoryContentForTab: async (_tab, ref) => {
        const gate = deferred<HistoryContentChunk>();
        requests.push({ revision: ref.revision, gate });
        started[requests.length - 1]?.resolve();
        return gate.promise;
      },
    });
    store.noteSessionBinding("tab", "/session", "session-generation-1");
    store.installSlice("tab", "/session", slice(1));
    const read = () => store.requestFullContent("tab", "m:answer", "content");
    return { store, requests, page, read, started };
  }
  {
    const { store, requests, read } = fixture();
    const pending = read();
    assert.equal(store.noteSessionBinding("tab", "/session", "session-generation-1"), false);
    assert.ok(store.peek("tab", "/session"), "the same binding retains its resident window");
    assert.equal(store.noteSessionBinding("tab", "/session", "session-generation-2"), true);
    assert.equal(store.peek("tab", "/session"), undefined, "same-path rebind invalidates the old cache before reading");
    store.installSlice("tab", "/session", slice(2));
    requests[0].gate.resolve(chunk("obsolete"));
    assert.equal(await pending, undefined, "a binding replacement cannot hand old body reads to the new session");
    assert.equal(requests.length, 1);
  }
  {
    const { store, requests, read, started } = fixture();
    const pending = read();
    store.installSlice("tab", "/session", slice(2));
    requests[0].gate.resolve(chunk("obsolete"));
    await started[1].promise;
    assert.equal(requests[1]?.revision, 2, "Follow installation hands the original alias to the new cut");
    requests[1].gate.resolve(chunk("current"));
    assert.equal(await pending, "current");
    assert.equal(await read(), "current", "resolved body remains resident");
  }
  {
    const { store, requests, read } = fixture();
    const pending = read();
    store.evictTab("tab");
    store.installSlice("tab", "/session", slice(2));
    requests[0].gate.resolve(chunk("obsolete"));
    assert.equal(await pending, undefined, "eviction is terminal even when the same tab reopens");
    assert.equal(requests.length, 1, "the old owner never starts a read for the new owner");
    assert.equal(store.peek("tab", "/session")?.items[0]?.kind, "assistant");
  }
  {
    const { store, requests, page, read, started } = fixture();
    const reload = store.loadLatest("tab", "/session");
    const pending = read();
    assert.equal(requests.length, 0, "a body requested during replacement waits for the new page");
    page.resolve(slice(2));
    await reload;
    await started[0].promise;
    assert.equal(requests[0]?.revision, 2, "replacement does not read an old ref under a new generation");
    requests[0].gate.resolve(chunk("fresh"));
    assert.equal(await pending, "fresh");
  }
  {
    const { store, requests, read, started } = fixture();
    const pending = read();
    store.installSlice("tab", "/session", slice(2));
    requests[0].gate.resolve(chunk("first"));
    await started[1].promise;
    store.installSlice("tab", "/session", slice(3));
    requests[1].gate.resolve(chunk("second"));
    assert.equal(await pending, undefined, "repeated replacement cannot create an unbounded retry loop");
    assert.equal(requests.length, 2, "one content request has at most one generation handoff");
  }
}
