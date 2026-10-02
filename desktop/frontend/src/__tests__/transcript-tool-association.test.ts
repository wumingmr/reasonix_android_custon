import assert from "node:assert/strict";
import test from "node:test";
import { TranscriptStore } from "../lib/transcriptStore";
import type { Item } from "../lib/useController";
import { FakeBackend } from "./helpers/transcriptFakeBackend";

const toolsOf = (items: Item[]) => items.filter((item): item is Extract<Item, { kind: "tool" }> => item.kind === "tool");

test("a persisted background-output read does not restart when its job snapshot was running", () => {
  const store = new TranscriptStore(new FakeBackend([]));
  const projection = store.installSlice("poll", "session-id:poll", {
    entries: [
      { entryId: "m:call", turn: 1, order: 0, message: { role: "assistant", content: "", toolCalls: [{ id: "poll", name: "job_output", arguments: "{}", resultObservation: { state: "completed", messageId: "result" } }] }, refs: [] },
      { entryId: "m:result", turn: 1, order: 1, message: { role: "tool", toolCallId: "poll", toolName: "job_output", messageId: "result", content: "[status: running]", execution: { state: "running" } }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "poll", stale: false,
  });
  assert.equal(toolsOf(projection.items)[0].status, "done");
});

test("formal message aliases retain one canonical result", () => {
  const store = new TranscriptStore(new FakeBackend([]));
  const projection = store.installSlice("formal-representation-alias", "/formal-representation-alias", {
    entries: [
      { entryId: "m:owner", turn: 1, order: 0, message: { role: "assistant", messageId: "owner", content: "", toolCalls: [{
        id: "call", name: "bash", arguments: "echo ok", resultObservation: { messageId: "result", state: "completed" },
      }] }, refs: [] },
      { entryId: "m:result", turn: 1, order: 1, message: { role: "tool", messageId: "result", toolCallId: "call", content: "ok" }, refs: [] },
      { entryId: "snapshot:result", turn: 1, order: 2, message: { role: "tool", messageId: "result", toolCallId: "call", content: "stale duplicate" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "formal-alias", stale: false,
  });
  const tools = toolsOf(projection.items);
  assert.equal(tools.length, 1);
  assert.equal(tools[0].id, "call");
  assert.equal(tools[0].output, "ok");
});

test("conflicting formal identities stay visible and independently readable", async () => {
  const store = new TranscriptStore(new FakeBackend([]));
  const projection = store.installSlice("identity-conflict", "/identity-conflict", {
    entries: [
      { entryId: "m:result-a", turn: 1, order: 0, message: { role: "tool", messageId: "result-a", toolCallId: "conflict", content: "a" }, refs: [] },
      { entryId: "m:result-b", turn: 1, order: 1, message: { role: "tool", messageId: "result-b", toolCallId: "conflict", content: "b" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "conflict", stale: false,
  });
  const tools = toolsOf(projection.items);
  assert.equal(store.residentWindowEntries(), 2);
  assert.equal(tools.length, 2);
  assert.equal(new Set(tools.map(item => item.id)).size, 2);
  assert.ok(tools.every(item => item.identityConflict));
  const details = await Promise.all(tools.map(item => store.requestToolContent("identity-conflict", item, { output: item.output })));
  assert.deepEqual(details.map(detail => JSON.parse(detail!).output).sort(), ["a", "b"]);
});

test("an ambiguous call and both formal results remain distinct", () => {
  const store = new TranscriptStore(new FakeBackend([]));
  const projection = store.installSlice("call-conflict", "/call-conflict", {
    entries: [
      { entryId: "m:owner", turn: 1, order: 0, message: { role: "assistant", messageId: "owner", content: "", toolCalls: [{ id: "conflict", name: "bash", arguments: "echo ok" }] }, refs: [] },
      { entryId: "m:result-a", turn: 1, order: 1, message: { role: "tool", messageId: "result-a", toolCallId: "conflict", content: "a" }, refs: [] },
      { entryId: "m:result-b", turn: 1, order: 2, message: { role: "tool", messageId: "result-b", toolCallId: "conflict", content: "b" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "call-conflict", stale: false,
  });
  const tools = toolsOf(projection.items);
  assert.equal(tools.length, 3);
  assert.equal(new Set(tools.map(item => item.id)).size, 3);
  assert.ok(tools.every(item => item.identityConflict));
});

test("a reused call id stays isolated by turn", () => {
  const store = new TranscriptStore(new FakeBackend([]));
  const projection = store.installSlice("reused-call", "/reused-call", {
    entries: [
      { entryId: "m:owner-1", turn: 1, order: 0, message: { role: "assistant", messageId: "owner-1", content: "", toolCalls: [{ id: "reused", name: "bash", arguments: "one" }] }, refs: [] },
      { entryId: "m:result-1", turn: 1, order: 1, message: { role: "tool", messageId: "result-1", toolCallId: "reused", content: "first" }, refs: [] },
      { entryId: "m:owner-2", turn: 2, order: 2, message: { role: "assistant", messageId: "owner-2", content: "", toolCalls: [{ id: "reused", name: "bash", arguments: "two" }] }, refs: [] },
      { entryId: "m:result-2", turn: 2, order: 3, message: { role: "tool", messageId: "result-2", toolCallId: "reused", content: "second" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 2, startTurn: 1, endTurn: 2,
    revision: 1, revisionKnown: true, digest: "reused-call", stale: false,
  });
  const tools = toolsOf(projection.items);
  assert.equal(tools.length, 2);
  assert.equal(new Set(tools.map(item => item.id)).size, 2);
  assert.deepEqual(tools.map(item => [item.args, item.output]), [["one", "first"], ["two", "second"]]);
  assert.ok(tools.every(item => !item.identityConflict));
});

test("paging a formal result replaces its resident event alias", async () => {
  const backend = new FakeBackend([]);
  backend.HistorySliceForTab = async () => ({
    entries: [
      { entryId: "m:result", turn: 1, order: 1, message: { role: "tool", messageId: "result", toolCallId: "page-call", toolName: "write_file", content: "full", execution: { state: "completed", durationMs: 99 } }, refs: [] },
      { entryId: "m:final", turn: 1, order: 2, message: { role: "assistant", messageId: "final", content: "done", turnFinal: true }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "page-alias", stale: false,
  });
  const store = new TranscriptStore(backend);
  store.installSlice("page-alias", "/page-alias", {
    entries: [
      { entryId: "m:owner", turn: 1, order: 0, message: { role: "assistant", messageId: "owner", content: "", toolCalls: [{ id: "page-call", name: "write_file", arguments: '{"path":"page.html"}' }] }, refs: [] },
      { entryId: "tool:page-call", turn: 1, order: 1, message: { role: "tool", toolCallId: "page-call", toolName: "write_file", content: "preview" }, refs: [] },
    ], nextCursor: "", newerCursor: "next", hasOlder: false, hasNewer: true, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "page-alias", stale: false,
  });
  const tools = toolsOf((await store.loadNewer("page-alias", "/page-alias"))!.items);
  assert.equal(tools.length, 1);
  assert.equal(tools[0].args, '{"path":"page.html"}');
  assert.equal(tools[0].execution?.durationMs, 99);
});

test("a lazy formal result joins through its observation before body load", () => {
  const store = new TranscriptStore(new FakeBackend([]));
  const projection = store.installSlice("lazy-result-identity", "/lazy-result-identity", {
    entries: [
      { entryId: "m:owner", turn: 1, order: 0, message: { role: "assistant", messageId: "owner", content: "", toolCalls: [{
        id: "lazy-result", name: "write_file", arguments: '{"path":"lazy.html"}',
        resultObservation: { state: "completed", messageId: "result", version: 1, contentRef: { digest: "body", bytes: 8192 } },
      }] }, refs: [] },
      { entryId: "m:result", turn: 1, order: 1, message: { role: "tool", messageId: "result", content: "written" },
        refs: [{ entryId: "m:result", field: "canonicalMessage", size: 8192, chunks: 1, revision: 1, digest: "body" }] },
      { entryId: "m:final", turn: 1, order: 2, message: { role: "assistant", messageId: "final", content: "done", turnFinal: true }, refs: [] },
      { entryId: "tool:lazy-result", turn: 1, order: 3, message: { role: "tool", toolCallId: "lazy-result", toolName: "write_file", content: "written" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "lazy-result", stale: false,
  });
  const tools = toolsOf(projection.items);
  assert.equal(tools.length, 1);
  assert.equal(tools[0].id, "lazy-result");
  assert.ok(projection.items.findIndex(item => item.id === "lazy-result") < projection.items.findIndex(item => item.id === "m:final"));
});
