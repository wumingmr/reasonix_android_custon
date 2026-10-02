import { FakeBackend, type RefTable } from "./helpers/transcriptFakeBackend";
// Run: tsx src/__tests__/transcript-store.test.ts
//
// TranscriptStore unit tests over a fake slice backend: stable ids, page
// concatenation fidelity vs the single-shot conversion, weighted LRU
// eviction, generation-bound request discard, stale cursors, lazy content
// refs, and the markdown cache budget.

import { TranscriptStore } from "../lib/transcriptStore";
import { verifyTranscriptContentOwnership } from "./helpers/transcriptContentOwnership";
import { historyPageRequestBudget } from "../lib/historyPaging";
import { historyMessagesToItems, type Item } from "../lib/useController";
import type {
  HistoryContentChunk,
  HistoryMessage,
  HistorySlice,
} from "../lib/types";

let passed = 0;
let failed = 0;

function ok(value: boolean, label: string) {
  if (value) {
    process.stdout.write(`  PASS  ${label}\n`);
    passed += 1;
  } else {
    process.stdout.write(`  FAIL  ${label}\n`);
    failed += 1;
  }
}

function eq(actual: unknown, expected: unknown, label: string) {
  ok(actual === expected, `${label}${actual === expected ? "" : `: expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`}`);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

// ── fake slice backend ──────────────────────────────────────────────────────



// ── fixtures ────────────────────────────────────────────────────────────────

function bigTranscript(turns: number): HistoryMessage[] {
  const messages: HistoryMessage[] = [];
  for (let i = 0; i < turns; i += 1) {
    messages.push({ role: "user", content: `prompt ${i}` });
    messages.push({ role: "assistant", content: `answer ${i}`, reasoning: `think ${i}` });
    messages.push({
      role: "assistant",
      content: "",
      toolCalls: [
        { id: `c${i}a`, name: "read_file", arguments: `{"path":"f${i}"}` },
        { id: `c${i}b`, name: "bash", arguments: `echo ${i}` },
      ],
    });
    messages.push({ role: "tool", toolCallId: `c${i}a`, toolName: "read_file", content: `read result ${i}` });
    messages.push({ role: "tool", toolCallId: `c${i}b`, toolName: "bash", content: `bash output ${i}` });
    if (i % 4 === 1) {
      // Positional (id-less) call/result pair.
      messages.push({ role: "assistant", content: "", toolCalls: [{ id: "", name: "grep", arguments: `needle ${i}` }] });
      messages.push({ role: "tool", toolName: "grep", content: `grep output ${i}` });
    }
    if (i % 3 === 0) messages.push({ role: "phase", content: `phase ${i}` });
    if (i % 5 === 2) messages.push({ role: "notice", level: "info", content: `note ${i}` });
    if (i % 7 === 3) messages.push({ role: "compaction", content: "", trigger: "auto", messages: 12, summary: `sum ${i}`, archive: `arch ${i}` });
  }
  return messages;
}

function longArchivedTranscript(turns: number): HistoryMessage[] {
  const messages: HistoryMessage[] = [];
  for (let turn = 1; turn <= turns; turn += 1) {
    const callId = `archived-${turn}`;
    messages.push({ role: "user", content: `prompt ${turn}` });
    messages.push({
      role: "assistant",
      content: `answer ${turn}`,
      toolCalls: [{
        id: callId,
        name: "bash",
        arguments: "",
        argumentsArchived: true,
        subject: `command ${turn}`,
        summary: "1 line",
      }],
    });
    messages.push({
      role: "tool",
      toolCallId: callId,
      toolName: "bash",
      content: "",
      toolResultArchived: true,
    });
  }
  return messages;
}

// Canonical shape for cross-scheme equality (ids are scheme-dependent and
// verified separately).
function canon(items: Item[]): unknown[] {
  return items.map((it) => {
    switch (it.kind) {
      case "user": return ["user", it.text, it.submitText ?? null];
      case "assistant": return ["assistant", it.text, it.reasoning];
      case "phase": return ["phase", it.text];
      case "notice": return ["notice", it.level, it.text, it.detail ?? null];
      case "compaction": return ["compaction", it.trigger, it.summary, it.archive];
      case "tool": return ["tool", it.name, it.args, it.output ?? null, it.error ?? null, it.status, it.subject ?? null, it.summary ?? null];
      case "extension": return ["extension", it.surfaceKey];
    }
  });
}

function canonEqual(a: Item[], b: Item[]): boolean {
  return JSON.stringify(canon(a)) === JSON.stringify(canon(b));
}

async function drainOlder(store: TranscriptStore, tabId: string, path: string, turns: number): Promise<void> {
  for (let guard = 0; guard < 100; guard += 1) {
    const result = await store.loadOlder(tabId, path, { turns });
    if (!result || result.kind !== "prepend" || !result.hasOlder) return;
  }
  throw new Error("paging did not terminate");
}

console.log("\ntranscript store");

{
  const store = new TranscriptStore(new FakeBackend([]));
  const projection = store.installSlice("tool-alias", "/tool-alias", {
    entries: [
      { entryId: "m:user", turn: 1, order: 0, message: { role: "user", messageId: "user", content: "build" }, refs: [] },
      { entryId: "m:owner", turn: 1, order: 1, message: { role: "assistant", messageId: "owner", content: "", toolCalls: [{ id: "call-1", name: "edit_file", arguments: '{"path":"blackhole.html"}' }] }, refs: [] },
      { entryId: "m:result", turn: 1, order: 2, message: { role: "tool", messageId: "result", toolCallId: "call-1", toolName: "edit_file", content: "written", execution: { state: "completed", durationMs: 8 } }, refs: [] },
      { entryId: "m:final", turn: 1, order: 3, message: { role: "assistant", messageId: "final", content: "done", turnFinal: true }, refs: [] },
      { entryId: "tool:call-1", turn: 1, order: 4, message: { role: "tool", toolCallId: "call-1", toolName: "edit_file", content: "written" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "tool-alias", stale: false,
  });
  const tools = projection.items.filter(item => item.kind === "tool" && item.id === "call-1");
  eq(tools.length, 1, "formal tool result and event alias project one stable node");
  eq(tools[0]?.kind === "tool" && tools[0].args, '{"path":"blackhole.html"}', "coalescing preserves full call arguments");
  eq(tools[0]?.kind === "tool" && tools[0].execution?.durationMs, 8, "coalescing preserves formal execution metadata");
  ok(projection.items.findIndex(item => item.id === "call-1") < projection.items.findIndex(item => item.id === "m:final"), "tool node remains at its call position before the final answer");
}

{
  const store = new TranscriptStore(new FakeBackend([]));
  store.installSlice("alias-boundary", "/alias-boundary", {
    entries: [
      { entryId: "m:final", turn: 1, order: 2, message: { role: "assistant", messageId: "final", content: "done" }, refs: [] },
      { entryId: "tool:boundary", turn: 1, order: 3, message: { role: "tool", toolCallId: "boundary", toolName: "bash", content: "preview" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "boundary", stale: false,
  });
  const projection = store.appendEntries("alias-boundary", "/alias-boundary", [
    { entryId: "m:owner", turn: 1, order: 0, message: { role: "assistant", messageId: "owner", content: "", toolCalls: [{ id: "boundary", name: "bash", arguments: "echo ok" }] }, refs: [] },
    { entryId: "m:result", turn: 1, order: 1, message: { role: "tool", messageId: "result", toolCallId: "boundary", toolName: "bash", content: "ok", execution: { state: "completed", exitCode: 0 } }, refs: [] },
  ]);
  eq(projection?.items.filter(item => item.id === "boundary").length, 1, "formal result crossing a window boundary supersedes its resident alias");
  const tool = projection?.items.find((item): item is Extract<Item, { kind: "tool" }> => item.kind === "tool" && item.id === "boundary");
  eq(tool?.args, "echo ok", "cross-boundary merge keeps call arguments");
  eq(tool?.execution?.exitCode, 0, "cross-boundary merge adopts formal execution metadata");
  ok((projection?.items.findIndex(item => item.id === "boundary") ?? -1) < (projection?.items.findIndex(item => item.id === "m:final") ?? -1), "cross-boundary merge restores call order");
}

{
  const store = new TranscriptStore(new FakeBackend([]));
  store.installSlice("identity-conflict", "/identity-conflict", {
    entries: [
      { entryId: "m:result-a", turn: 1, order: 0, message: { role: "tool", messageId: "result-a", toolCallId: "conflict", content: "a" }, refs: [] },
      { entryId: "m:result-b", turn: 1, order: 1, message: { role: "tool", messageId: "result-b", toolCallId: "conflict", content: "b" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "conflict", stale: false,
  });
  eq(store.residentWindowEntries(), 2, "identity conflict does not silently discard a formal record");
}

{
  const backend = new FakeBackend([]);
  const canonical = JSON.stringify({ id: "owner", role: "assistant", content: "",
    tool_calls: [{ id: "lazy-call", name: "write_file", arguments: '{"path":"late.html"}' }] });
  backend.HistoryContentForTab = async (_tab, ref, chunk) => ({ entryId: ref.entryId, field: ref.field, chunk,
    chunks: 1, data: canonical, done: true, stale: false });
  const store = new TranscriptStore(backend);
  const changes: import("../lib/transcriptStore").TranscriptContentChange[] = [];
  store.subscribe("lazy-owner", change => changes.push(change));
  const before = store.installSlice("lazy-owner", "/lazy-owner", {
    entries: [
      { entryId: "m:owner", turn: 1, order: 0, message: { role: "assistant", messageId: "owner", content: "" },
        refs: [{ entryId: "m:owner", field: "canonicalMessage", size: canonical.length, chunks: 1, revision: 1, digest: "lazy" }] },
      { entryId: "m:final", turn: 1, order: 1, message: { role: "assistant", messageId: "final", content: "done", turnFinal: true }, refs: [] },
      { entryId: "m:result", turn: 1, order: 2, message: { role: "tool", messageId: "result", toolCallId: "lazy-call", toolName: "write_file", content: "written" }, refs: [] },
    ], nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "lazy", stale: false,
  });
  ok(before.items.findIndex(item => item.id === "lazy-call") > before.items.findIndex(item => item.id === "m:final"), "unresolved call initially leaves its result at the historical result position");
  await store.requestFullContent("lazy-owner", "m:owner", "canonicalMessage");
  const after = store.peek("lazy-owner", "/lazy-owner")!;
  eq(after.items.filter(item => item.id === "lazy-call").length, 1, "lazy canonical body keeps one tool key after ownership changes");
  ok(after.items.findIndex(item => item.id === "lazy-call") < after.items.findIndex(item => item.id === "m:final"), "lazy canonical body moves the tool to its call position");
  eq(after.items.find(item => item.id === "lazy-call")?.kind === "tool" && (after.items.find(item => item.id === "lazy-call") as Extract<Item, { kind: "tool" }>).args,
    '{"path":"late.html"}', "lazy canonical body restores full call arguments");
  ok(Boolean(changes[changes.length - 1]?.projection), "lazy ownership changes publish one authoritative structural projection");
}

{
  const messages: HistoryMessage[] = [
    { role: "user", content: "read all" },
    { role: "assistant", content: "candidate answer" },
    { role: "notice", content: "", code: "incomplete_read", readPause: { id: "run", reads: [{ readId: "r", path: "fixture.txt", reason: "no_progress" }] } },
  ];
  const store = new TranscriptStore(new FakeBackend(messages));
  const first = await store.loadLatest("read", "/read.jsonl", { turns: 12 });
  const expected = historyMessagesToItems(messages, "history").items.find(i => i.kind === "notice");
  const actual = first?.items.find(i => i.kind === "notice");
  eq(JSON.stringify(actual), JSON.stringify(expected), "paged read pause equals live and legacy history presentation");
  const replay = await store.loadLatest("read", "/read.jsonl", { turns: 12 });
  eq(replay?.items.filter(i => i.kind === "notice").length, 1, "reloading a pause does not duplicate its card");
}

// ── page concatenation equals single-shot conversion ────────────────────────
// This is a conversion-fidelity property, not a residency one: paging a whole
// transcript in must project exactly what one single-shot conversion produces.
// The window is deliberately unbounded here so the comparison sees every page;
// the bounded-window behaviour is covered separately below.
{
  const messages = bigTranscript(46);
  const backend = new FakeBackend(messages);
  const store = new TranscriptStore(backend, { windowMaxPages: 1_000 });
  const first = await store.loadLatest("tab-1", "/s/one.jsonl", { turns: 12 });
  ok(!!first && first.items.length > 0, "latest page projects items");
  eq(first?.hasOlder, true, "latest page reports older history");
  const projectedTurns = (first?.items ?? [])
    .filter((item): item is Extract<Item, { kind: "user" }> => item.kind === "user")
    .map((item) => item.historyTurn);
  eq(projectedTurns[projectedTurns.length - 1], 46, "history user items retain their absolute turn for complete-session navigation");
  ok(projectedTurns.every((turn) => Number.isInteger(turn) && (turn ?? 0) > 0), "every paged user item carries an absolute history turn");
  const firstIds = (first?.items ?? []).map((item) => item.id);
  await drainOlder(store, "tab-1", "/s/one.jsonl", 12);
  const full = store.peek("tab-1", "/s/one.jsonl");
  const singleShot = historyMessagesToItems(messages, "h").items;
  ok(canonEqual(full?.items ?? [], singleShot), `paged concatenation equals single-shot conversion (${singleShot.length} items from ${messages.length} messages)`);
  const fullIds = (full?.items ?? []).map((item) => item.id);
  eq(JSON.stringify(fullIds.slice(fullIds.length - firstIds.length)), JSON.stringify(firstIds), "newest page item ids are stable across prepends");
  const unique = new Set(fullIds);
  eq(unique.size, fullIds.length, "item ids are unique across the full projection");
}

// ── 10,000-turn targeted paging stays bounded ──────────────────────────────
{
  const messages = longArchivedTranscript(10_000);
  const backend = new FakeBackend(messages, new Map(), "stress");
  const store = new TranscriptStore(backend);
  const startedAt = performance.now();
  let projection = await store.loadLatest("tab-stress", "/s/stress.jsonl", { turns: 60 });
  let pages = 1;
  while (projection?.hasOlder && pages <= 40) {
    const budget = historyPageRequestBudget(projection.startTurn, projection.totalTurns, 1);
    const older = await store.loadOlder("tab-stress", "/s/stress.jsonl", budget);
    if (!older) break;
    projection = older;
    pages += 1;
  }
  const elapsedMs = performance.now() - startedAt;
  const users = (projection?.items ?? []).filter((item): item is Extract<Item, { kind: "user" }> => item.kind === "user");
  eq(projection?.hasOlder, false, "10,000-turn target paging reaches the first page");
  eq(pages, 32, "10,000-turn target paging respects both turn and production entry bounds");
  eq(backend.sliceCalls.length, 32, "10,000-turn target paging performs the expected bounded backend calls");
  ok(backend.sliceCalls.slice(1).every((request) => request.entries === 1000), "targeted pages use the backend's bounded 1000-entry capacity");
  eq(users[0]?.historyTurn, 1, "10,000-turn target paging lands on absolute turn one");
  ok(new Set(projection?.items.map((item) => item.id)).size === projection?.items.length, "10,000-turn target paging keeps item ids unique");
  const stats = store.stats();
  ok(stats.bodyBytes <= stats.bodyBudgetBytes, "10,000-turn transcript stays within the production history body budget");
  ok(elapsedMs < 10_000, `10,000-turn targeted paging completes within 10s (${elapsedMs.toFixed(1)}ms)`);

  // Reading 32 pages deep leaves a bounded window, not the whole session. The
  // reclaimed range is reported as still-newer rather than lost, and paging
  // forward from it restores the tail — full reachability, bounded residency.
  ok(stats.residentWindowEntries <= stats.windowMaxPages * 1000, `window residency is bounded (${stats.residentWindowEntries} entries, max ${stats.windowMaxPages * 1000})`);
  ok(stats.reclaimedPages > 0, "deep paging reclaimed pages instead of holding every page");
  eq(projection?.hasNewer, true, "the reclaimed tail is reported as still newer");
  const forward = await store.loadNewer("tab-stress", "/s/stress.jsonl", { entries: 1000 });
  eq(forward?.kind, "append", "paging forward appends into the same window");
  const forwardUsers = (forward?.appendItems ?? []).filter((item): item is Extract<Item, { kind: "user" }> => item.kind === "user");
  ok(forwardUsers.length > 0, "paging forward restores newer history after a reclaim");
  const lastForward = forwardUsers[forwardUsers.length - 1];
  const lastExisting = users[users.length - 1];
  ok((lastForward?.historyTurn ?? 0) > (lastExisting?.historyTurn ?? 0), "paging forward moves the window toward the live tail");
}

// ── cross-page tool call/result merge ───────────────────────────────────────
{
  const messages: HistoryMessage[] = [
    { role: "user", content: "p1" },
    { role: "assistant", content: "", toolCalls: [{ id: "call-1", name: "bash", arguments: "ls" }] },
    { role: "tool", toolCallId: "call-1", toolName: "bash", content: "/root" },
    { role: "user", content: "p2" },
    { role: "assistant", content: "done" },
  ];
  // Cut between the call and its result: newest page starts at the result row.
  const backend = new FakeBackend(messages);
  backend.HistorySliceForTab = async (tabID, req) => {
    void tabID;
    if (!req.cursor) return backend.slice(2, messages.length);
    const decoded = JSON.parse(atob(req.cursor)) as { before?: number };
    return backend.slice(0, Math.min(decoded.before ?? 0, 2));
  };
  const store = new TranscriptStore(backend);
  const first = await store.loadLatest("tab-x", "/s/x.jsonl", { turns: 12 });
  const standalone = (first?.items ?? []).filter((item) => item.kind === "tool");
  eq(standalone.length, 1, "result row converts standalone before its call pages in");
  eq(standalone[0]?.kind === "tool" && standalone[0].id, "call-1", "standalone result keeps the toolCallId item id");
  const older = await store.loadOlder("tab-x", "/s/x.jsonl", { turns: 12 });
  eq(older?.kind, "prepend", "older page prepends");
  eq(older?.removeIds.length, 1, "the standalone result item is superseded by the merged call item");
  const merged = (older?.items ?? []).filter((item) => item.kind === "tool");
  eq(merged.length, 1, "exactly one tool item after the merge (no duplicate)");
  const tool = merged[0]?.kind === "tool" ? merged[0] : undefined;
  eq(tool?.args, "ls", "merged tool item takes the call's args");
  eq(tool?.output, "/root", "merged tool item takes the result's output");
  eq(tool?.status, "done", "merged tool item is done");
  const singleShot = historyMessagesToItems(messages, "h").items;
  ok(canonEqual(older?.items ?? [], singleShot), "merged projection equals single-shot conversion");
}

// ── append (live tail) ──────────────────────────────────────────────────────
{
  const messages: HistoryMessage[] = [
    { role: "user", content: "p1" },
    { role: "assistant", content: "a1" },
  ];
  const backend = new FakeBackend(messages);
  const store = new TranscriptStore(backend);
  const first = await store.loadLatest("tab-a", "/s/a.jsonl", { turns: 12 });
  const baseIds = (first?.items ?? []).map((item) => item.id);
  const appended = store.appendEntries("tab-a", "/s/a.jsonl", [
    { entryId: "s1:r0:m2:o0", turn: 2, order: 2, message: { role: "user", content: "p2" }, refs: [] },
    { entryId: "s1:r0:m3:o0", turn: 2, order: 3, message: { role: "assistant", content: "a2" }, refs: [] },
  ]);
  eq(appended?.items.length, baseIds.length + 2, "append contributes the new rows' items");
  const projection = store.peek("tab-a", "/s/a.jsonl");
  eq(JSON.stringify((projection?.items ?? []).slice(0, baseIds.length).map((item) => item.id)), JSON.stringify(baseIds), "append keeps existing item ids");
  eq(projection?.items.length, baseIds.length + 2, "append grows the projection");
}

// ── long-running live tail uses the same three-page residency budget ────────
{
  const backend = new FakeBackend([{ role: "user", content: "seed" }, { role: "assistant", content: "seed answer" }]);
  const store = new TranscriptStore(backend, { windowMaxPages: 3, windowPageEntries: 4 });
  await store.loadLatest("tab-live", "/s/live.jsonl", { turns: 12 });
  let reclaimed = 0;
  for (let batch = 0; batch < 8; batch += 1) {
    const turn = batch + 2;
    const result = store.appendEntries("tab-live", "/s/live.jsonl", [
      { entryId: `live-u-${turn}`, turn, order: turn * 2, message: { role: "user", content: `p${turn}` }, refs: [] },
      { entryId: `live-a-${turn}`, turn, order: turn * 2 + 1, message: { role: "assistant", content: `a${turn}` }, refs: [] },
    ]);
    reclaimed += result?.removeIds.length ?? 0;
  }
  const projection = store.peek("tab-live", "/s/live.jsonl");
  ok((projection?.items.length ?? 0) <= 12, "live tail remains inside three four-entry pages");
  ok(reclaimed > 0, "live append reports mounted ids reclaimed from the oldest edge");
  ok((projection?.startTurn ?? 0) > 1, "live window advances its visible start turn after reclaim");
  eq(projection?.endTurn, 9, "live window retains the latest settled turn");
}

// ── weighted LRU: count, pin, byte budget, re-open ──────────────────────────
{
  const store = new TranscriptStore(new FakeBackend([]));
  store.installSlice("tab-aba", "/s/same.jsonl", {
    entries: [{ entryId: "m:old", turn: 1, order: 0, message: { role: "user", content: "old generation" }, refs: [] }],
    nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false,
    startTurn: 1, endTurn: 1, totalTurns: 1, revision: 4, digest: "digest-v4", stale: false,
  });
  ok(Boolean(store.peek("tab-aba", "/s/same.jsonl", { revision: 4, digest: "digest-v4" })), "matching fingerprint serves the resident projection");
  eq(store.peek("tab-aba", "/s/same.jsonl", { revision: 5, digest: "digest-v5" }), undefined, "same-path ABA fingerprint mismatch is a cache miss");
}

{
  const store = new TranscriptStore(new FakeBackend([]));
  const initial = store.installSlice("reader-v2", "/reader", {
    entries: [{ entryId: "m:old", turn: 1, order: 0, message: { role: "user", content: "old reader page" }, refs: [] }],
    nextCursor: "", newerCursor: "newer", hasOlder: false, hasNewer: true,
    startTurn: 1, endTurn: 1, totalTurns: 100, revision: 1, digest: "generation", stale: false,
  });
  for (let sequence = 2; sequence < 130; sequence++) {
    const updated = store.upsertEntries("reader-v2", "/reader", [{ entryId: `m:tail-${sequence}`, turn: sequence, order: sequence,
      message: { role: "assistant", content: "committed tail" }, refs: [] }], sequence);
    eq(updated?.items.length, initial.items.length, "distant commits do not replace or grow the reader window");
    eq(updated?.items[0]?.id, initial.items[0]?.id, "reader anchor survives distant commits");
  }
  eq(store.peek("reader-v2", "/reader")?.hasNewer, true, "committed tail remains reachable by forward pagination");
}

{
  const backend = new FakeBackend([{ role: "user", content: "u" }, { role: "assistant", content: "a" }]);
  const store = new TranscriptStore(backend, { maxResidentSessions: 3 });
  await store.loadLatest("tab-1", "/s/1.jsonl");
  await store.loadLatest("tab-2", "/s/2.jsonl");
  await store.loadLatest("tab-3", "/s/3.jsonl");
  eq(store.residentSessionCount(), 3, "three sessions resident at the cap");
  await store.loadLatest("tab-4", "/s/4.jsonl");
  eq(store.isResident("tab-1", "/s/1.jsonl"), false, "fourth session evicts the least-recently-used one");
  eq(store.isResident("tab-4", "/s/4.jsonl"), true, "new session stays resident");

  store.setPinned("tab-2", true); // live/running owner cannot be evicted
  await store.loadLatest("tab-5", "/s/5.jsonl");
  eq(store.isResident("tab-3", "/s/3.jsonl"), false, "pinned windows consume capacity without being eviction candidates");
  eq(store.residentSessionCount(), 3, "resident cap includes the pinned window");
  await store.loadLatest("tab-6", "/s/6.jsonl");
  eq(store.isResident("tab-2", "/s/2.jsonl"), true, "pinned live session survives eviction");
  eq(store.isResident("tab-4", "/s/4.jsonl"), false, "oldest unpinned session evicts instead");
  store.setPinned("tab-2", false);

  const callsBeforeReopen = backend.sliceCalls.length;
  const reopened = await store.loadLatest("tab-1", "/s/1.jsonl");
  ok(backend.sliceCalls.length > callsBeforeReopen, "evicted session re-opens via a fresh slice fetch");
  eq(reopened?.items.length, 2, "re-opened session restores its full projection");
}


{
  const big = "x".repeat(600);
  const backend = new FakeBackend([{ role: "user", content: big }, { role: "assistant", content: big }]);
  const store = new TranscriptStore(backend, { maxResidentSessions: 10, historyBodyBudgetBytes: 4096 });
  await store.loadLatest("tab-1", "/s/1.jsonl");
  await store.loadLatest("tab-2", "/s/2.jsonl");
  await store.loadLatest("tab-3", "/s/3.jsonl");
  ok(store.totalBodyBytes() <= 4096, "history body budget holds across sessions");
  eq(store.isResident("tab-1", "/s/1.jsonl"), false, "byte budget evicts the oldest by weight");
  eq(store.isResident("tab-3", "/s/3.jsonl"), true, "newest session survives byte-budget eviction");
}

// ── markdown cache budget + LRU ─────────────────────────────────────────────
{
  const store = new TranscriptStore(new FakeBackend([]), { markdownBudgetBytes: 120 });
  const parsed = (text: string) => ({
    source: text,
    blocks: [],
    selectionText: text,
    selectionRevision: 1,
    bytes: text.length * 2,
  });
  store.setMarkdown(1, parsed("a".repeat(20))); // 40 bytes
  store.setMarkdown(2, parsed("b".repeat(20)));
  store.setMarkdown(3, parsed("c".repeat(20)));
  eq(store.getMarkdown("a".repeat(20), 1)?.source, "a".repeat(20), "markdown cache returns stored value");
  store.setMarkdown(4, parsed("d".repeat(20))); // 160 > 120 → evict oldest (2: 1 was touched)
  eq(store.getMarkdown("b".repeat(20), 2), undefined, "markdown LRU evicts the least-recently-used entry");
  ok(store.getMarkdown("a".repeat(20), 1) !== undefined, "recently read markdown entry survives");
  eq(store.getMarkdown("x".repeat(20), 1), undefined, "a revision collision with different source text is a miss");

  const release = store.pinMarkdown(1);
  store.setMarkdown(5, parsed("e".repeat(50)));
  ok(store.getMarkdown("a".repeat(20), 1) !== undefined, "active selection pins its markdown projection");
  release();
}

// ── lazy content refs ───────────────────────────────────────────────────────
{
  const full = "FULL-".repeat(40); // 200 chars
  const refs: RefTable = new Map([["s1:r0:m1:o0:content", full]]);
  const backend = new FakeBackend(
    [{ role: "user", content: "p1" }, { role: "assistant", content: "placeholder" }],
    refs,
  );
  const store = new TranscriptStore(backend);
  const changes: string[] = [];
  store.subscribe("tab-c", (change) => changes.push(...(change.projection?.items.map(item => item.id) ?? Object.keys(change.patches))));
  const first = await store.loadLatest("tab-c", "/s/c.jsonl", { turns: 12 });
  // ChatContentLoader owns automatic body reads and the four-request budget.
  // The store must not eagerly bypass it or load closed thought/tool fields.
  await new Promise((resolve) => setTimeout(resolve, 0));
  eq(backend.contentCalls.length, 0, "newest-page references stay lazy until the view requests them");
  eq(store.hasContentReference("tab-c", "s1:r0:m1:o0", "content"), true, "the view can distinguish a missing full value from an unreferenced body");
  await store.requestFullContent("tab-c", "s1:r0:m1:o0", "content");
  eq(first?.hasOlder, false, "fixture fits in one page");
  const assistant = (store.peek("tab-c", "/s/c.jsonl")?.items ?? []).find((item) => item.kind === "assistant");
  eq(assistant?.kind === "assistant" && assistant.text, full, "resolved full content replaces the inline preview");
  ok(changes.includes("he:s1:r0:m1:o0"), "content resolution notifies subscribers with item patches");
  const again = await store.requestFullContent("tab-c", "s1:r0:m1:o0", "content");
  eq(again, full, "resolved content is served from the record");
  eq(backend.contentCalls.length, 2, "resolved content is not re-fetched");
}

{
  // Stale content fetch: ref marked stale, preview kept.
  const refs: RefTable = new Map([["s1:r0:m1:o0:content", "z".repeat(100)]]);
  const backend = new FakeBackend(
    [{ role: "user", content: "p1" }, { role: "assistant", content: "placeholder" }],
    refs,
  );
  backend.HistoryContentForTab = async (_tab, ref, chunk) => ({ entryId: ref.entryId, field: ref.field, chunk, chunks: 2, data: "", done: false, stale: true });
  const store = new TranscriptStore(backend);
  const first = await store.loadLatest("tab-s", "/s/s.jsonl", { turns: 12 });
  await new Promise((resolve) => setTimeout(resolve, 0));
  const assistant = (store.peek("tab-s", "/s/s.jsonl")?.items ?? []).find((item) => item.kind === "assistant");
  eq(assistant?.kind === "assistant" && assistant.text, "z".repeat(16), "stale ref keeps the inline preview");
  eq(first !== undefined, true, "latest page still projects");
}

// ── generation: superseded / evicted loads discard late responses ───────────
{
  const backend = new FakeBackend([{ role: "user", content: "u" }, { role: "assistant", content: "a" }]);
  const store = new TranscriptStore(backend);
  backend.sliceGate = deferred<HistorySlice>();
  const firstGate = backend.sliceGate;
  const p1 = store.loadLatest("tab-g", "/s/g.jsonl");
  backend.sliceGate = deferred<HistorySlice>();
  const secondGate = backend.sliceGate;
  const p2 = store.loadLatest("tab-g", "/s/g.jsonl"); // supersedes: bumps generation
  firstGate.resolve(backend.slice(0, 2));
  eq(await p1, undefined, "superseded load discards its late response");
  secondGate.resolve(backend.slice(0, 2));
  const projection = await p2;
  eq(projection?.items.length, 2, "the latest load wins");

  backend.sliceGate = deferred<HistorySlice>();
  const gate = backend.sliceGate;
  const p3 = store.loadLatest("tab-h", "/s/h.jsonl");
  store.evictTab("tab-h"); // pruned/closed before the response lands
  gate.resolve(backend.slice(0, 2));
  eq(await p3, undefined, "evicted session discards its late response");
  eq(store.isResident("tab-h", "/s/h.jsonl"), false, "evicted records never land");
}

{
  // A content request spanning a fresh load discards the old chunk and
  // transparently retries against the replacement generation.
  const full = "y".repeat(80);
  const refs: RefTable = new Map([["s1:r0:m1:o0:content", full]]);
  const backend = new FakeBackend(
    [{ role: "user", content: "p1" }, { role: "assistant", content: "placeholder" }],
    refs,
  );
  const store = new TranscriptStore(backend);
  backend.contentGate = deferred<HistoryContentChunk>();
  const staleGate = backend.contentGate;
  await store.loadLatest("tab-l", "/s/l.jsonl", { turns: 12 });
  const first = store.requestFullContent("tab-l", "s1:r0:m1:o0", "content");
  await new Promise((resolve) => setTimeout(resolve, 0));
  eq(backend.contentCalls.length, 1, "the requested first-generation content is in flight");
  // A fresh load (session switch/rebind) bumps the generation while the first
  // load's content request is still awaiting its chunk.
  const reload = store.loadLatest("tab-l", "/s/l.jsonl", { turns: 12 });
  staleGate.resolve({ entryId: "s1:r0:m1:o0", field: "content", chunk: 0, chunks: 2, data: "STALE", done: true, stale: false });
  const resolved = await first;
  await reload;
  const assistant = (store.peek("tab-l", "/s/l.jsonl")?.items ?? []).find((item) => item.kind === "assistant");
  eq(resolved, full, "generation rollover retries the original request against the replacement record");
  eq(backend.contentCalls.length, 3, "the replacement generation fetches both content chunks once");
  eq(assistant?.kind === "assistant" && assistant.text, full, "late content chunk from a previous generation is discarded");
}

// ── stale cursor reloads from the latest page ───────────────────────────────
{
  const messages: HistoryMessage[] = [];
  for (let i = 0; i < 30; i += 1) {
    messages.push({ role: "user", content: `p${i}` });
    messages.push({ role: "assistant", content: `a${i}` });
  }
  const backend = new FakeBackend(messages);
  const store = new TranscriptStore(backend);
  await store.loadLatest("tab-r", "/s/r.jsonl", { turns: 10 });
  backend.staleNextCursor = true; // the session was rewritten behind the cursor
  const result = await store.loadOlder("tab-r", "/s/r.jsonl", { turns: 10 });
  eq(result?.kind, "reload", "stale cursor triggers a latest-page reload");
  eq(result?.items.length, 20, "reload replaces with the fresh newest page");
  backend.staleNextCursor = false;
  const older = await store.loadOlder("tab-r", "/s/r.jsonl", { turns: 10 });
  eq(older?.kind, "prepend", "paging resumes after the reload");
  eq(older?.prependItems.length, 20, "older page prepends after reload");
}

// ── same-path resident identity ────────────────────────────────────────────
{
  const backend = new FakeBackend([{ role: "user", content: "u" }, { role: "assistant", content: "a" }]);
  const store = new TranscriptStore(backend);
  await store.loadLatest("tab-fp", "/s/fp.jsonl", { expectedRevision: 1, expectedDigest: "digest-1" });
  const callsAfterFirstLoad = backend.sliceCalls.length;
  const resident = await store.loadLatest("tab-fp", "/s/fp.jsonl", {
    preferResident: true,
    expectedRevision: 1,
    expectedDigest: "digest-1",
  });
  eq(backend.sliceCalls.length, callsAfterFirstLoad, "matching canonical fingerprint reuses the resident projection");
  eq(resident?.revision, 1, "resident projection retains its canonical revision");

  backend.revision = 2;
  backend.digest = "digest-2";
  const refreshed = await store.loadLatest("tab-fp", "/s/fp.jsonl", {
    preferResident: true,
    expectedRevision: 2,
    expectedDigest: "digest-2",
  });
  eq(backend.sliceCalls.length, callsAfterFirstLoad + 1, "changed same-path fingerprint bypasses the resident projection");
  eq(refreshed?.digest, "digest-2", "fresh projection adopts the advanced canonical digest");

  backend.HistorySliceForTab = async () => ({ ...backend.slice(0, 2), revision: 3, revisionKnown: undefined, digest: "digest-3" });
  const compatible = await store.loadLatest("tab-fp", "/s/fp.jsonl", {
    preferResident: true,
    expectedRevision: 3,
    expectedDigest: "digest-3",
  });
  eq(compatible?.revisionKnown, true, "positive legacy slice revision implies a known canonical identity");
}

// ── canonical ownership survives ephemeral tab replacement ────────────────
{
  const backend = new FakeBackend([{ role: "user", content: "warm A" }, { role: "assistant", content: "answer A" }]);
  const store = new TranscriptStore(backend);
  const sessionA = "s\0local\0session-a\0" + "0";
  store.noteSessionBinding("tab-a-1", "/same/path.jsonl", sessionA);
  await store.loadLatest("tab-a-1", "/same/path.jsonl", {
    expectedRevision: 1,
    expectedDigest: "digest-1",
  });
  const callsAfterWarm = backend.sliceCalls.length;
  store.evictTab("tab-a-1");

  store.noteSessionBinding("tab-a-3", "/same/path.jsonl", sessionA);
  const rebound = store.peek("tab-a-3", "/same/path.jsonl", {
    revision: 1,
    digest: "digest-1",
  });
  eq(rebound?.items.find(item => item.kind === "user")?.id, "he:s1:r0:m0:o0", "new tab id reuses the stable session resident projection");
  eq(backend.sliceCalls.length, callsAfterWarm, "stable session rebind paints without a full history read");
  eq(store.residentSessionCount(), 1, "stable rebind does not duplicate the resident session");
  eq(store.peek("tab-a-1", "/same/path.jsonl"), undefined, "old tab binding cannot address the rebound resident session");

  const staleFollowerAppend = store.appendEntries("tab-a-1", "/same/path.jsonl", [{
    entryId: "old:follower", turn: 2, order: 2, message: { role: "user", content: "late" }, refs: [],
  }]);
  eq(staleFollowerAppend, undefined, "old follower events are fenced after the tab rebind");

  backend.revision = 2;
  backend.digest = "digest-2";
  const refreshed = await store.loadLatest("tab-a-3", "/same/path.jsonl", {
    preferResident: true,
    expectedRevision: 2,
    expectedDigest: "digest-2",
  });
  eq(backend.sliceCalls.length, callsAfterWarm + 1, "changed canonical fingerprint reloads after a stable rebind");
  eq(refreshed?.digest, "digest-2", "rebound session installs the new canonical fingerprint");

  store.evictTab("tab-a-3");
  store.noteSessionBinding("tab-b", "/same/path.jsonl", "s\0local\0session-b\0" + "0");
  eq(store.peek("tab-b", "/same/path.jsonl", { revision: 2, digest: "digest-2" }), undefined, "same path with a different SessionID never reuses the resident projection");
  eq(store.peek("tab-b", "/same/path.jsonl", {}), undefined, "missing canonical fingerprint cannot manufacture a warm hit");
}

// A lazy body request belongs to the tab binding that started it, not merely
// to the stable resident object retained for the next tab.
{
  const full = "canonical body ".repeat(16);
  const refs = new Map<string, string>([["s1:r0:m0:o0:content", full]]);
  const backend = new FakeBackend([{ role: "assistant", content: full }], refs);
  const store = new TranscriptStore(backend);
  const stable = "s\0local\0session-content\0" + "0";
  store.noteSessionBinding("content-old", "/content.jsonl", stable);
  await store.loadLatest("content-old", "/content.jsonl");
  const contentGate = deferred<HistoryContentChunk>();
  backend.contentGate = contentGate;
  const pending = store.requestFullContent("content-old", "s1:r0:m0:o0", "content");
  store.evictTab("content-old");
  store.noteSessionBinding("content-new", "/content.jsonl", stable);
  contentGate.resolve({
    entryId: "s1:r0:m0:o0", field: "content", chunk: 0, chunks: 2, data: full, done: true, stale: false,
  });
  eq(await pending, undefined, "late lazy content from the old tab is discarded after canonical rebind");
}

// Legacy tool references are call-specific and never expand hidden siblings or
// retain fetched full bodies in the controller's contribution map.
{
  const args = "a".repeat(70000), output = "o".repeat(80000);
  const backend = new FakeBackend([
    { role: "user", content: "read" },
    { role: "assistant", content: "", toolCalls: [
      { id: "one", name: "bash", arguments: "args preview" }, { id: "two", name: "bash", arguments: "other preview" },
    ] },
    { role: "tool", toolCallId: "one", content: "output preview" },
  ]);
  const slice = backend.slice(0, 3);
  slice.entries![1].refs = ["one", "two"].map(toolCallId => ({ entryId: "s1:r0:m1:o0", toolCallId, field: "toolArguments", size: args.length, chunks: 1, revision: 1, digest: "d" }));
  slice.entries![2].refs = [{ entryId: "s1:r0:m2:o0", field: "content", size: output.length, chunks: 1, revision: 1, digest: "d" }];
  backend.HistorySliceForTab = async () => slice;
  let reads = 0;
  backend.HistoryContentForTab = async (_, ref) => {
    if (ref.toolCallId === "two") throw new Error("unopened call must stay lazy");
    reads++;
    return { entryId: ref.entryId, field: ref.field, chunk: 0, chunks: 1, data: ref.field === "content" ? output : args, done: true, stale: false };
  };
  const store = new TranscriptStore(backend);
  const view = await store.loadLatest("legacy", "/legacy");
  const item = view?.items.find((item): item is Extract<Item, { kind: "tool" }> => item.kind === "tool" && item.id === "one");
  if (!item) throw new Error("legacy tool missing");
  for (let attempt = 0; attempt < 2; attempt++) {
    const value = JSON.parse((await store.requestToolContent("legacy", item, { args: item.args, output: item.output }))!);
    eq(value.args, args, "legacy tool parameters load completely");
    eq(value.output, output, "legacy tool output loads completely");
  }
  eq(reads, 4, "reopening reads only the selected tool's two references");
  eq(store.peek("legacy", "/legacy")?.items.find(candidate => candidate.id === "one"), item, "full details leave the preview Item unchanged");
}

// ── reclaiming a page never strands a tool result ──────────────────────────
// A result row whose call was reclaimed names a call the reader can no longer
// see. Pages here are 2 messages wide over 3-message turns, so page boundaries
// fall between a call and its result and the reclaim has to widen past it.
{
  const messages: HistoryMessage[] = [];
  for (let i = 0; i < 12; i += 1) {
    messages.push({ role: "user", content: `q${i}` });
    messages.push({ role: "assistant", content: "", toolCalls: [{ id: `call-${i}`, name: "bash", arguments: `run ${i}` }] });
    messages.push({ role: "tool", toolCallId: `call-${i}`, toolName: "bash", content: `out ${i}` });
  }
  const backend = new FakeBackend(messages);
  const store = new TranscriptStore(backend, { windowMaxPages: 2 });
  const residentIds = () => new Set((store.peek("tab-tool", "/s/tool.jsonl")?.items ?? []).map((item) => item.id));

  // Page back to the head. Page [0,2) holds turn 0's call; the page after it
  // starts with that call's result, so the boundary splits the pair.
  await store.loadLatest("tab-tool", "/s/tool.jsonl", { entries: 2 });
  for (let page = 0; page < 40; page += 1) {
    if (!await store.loadOlder("tab-tool", "/s/tool.jsonl", { entries: 2 })) break;
  }
  const atHead = residentIds();
  ok(atHead.size > 0, "paging reaches the head of the transcript");

  // Growing forward reclaims the head page. The result that belonged to a call
  // on that page has to go with it, or the reader keeps an output row whose
  // call is no longer on screen.
  const newer = await store.loadNewer("tab-tool", "/s/tool.jsonl", { entries: 2 });
  ok(newer?.kind === "append", "paging forward appends after reaching the head");
  ok(store.stats().reclaimedPages > 0, "growing forward reclaimed a page");
  const afterReclaim = residentIds();
  for (const id of atHead) {
    if (!/^call-\d+$/.test(id)) continue;
    ok(!afterReclaim.has(id), `reclaimed call ${id} did not leave its result behind`);
  }
  ok(store.stats().residentWindowEntries <= 2 * 2, "the window stayed at its page budget");
}

// A completed result may live outside the resident page. Its locator supplies
// execution evidence, while the body is fetched only when explicitly expanded.
{
 const output = "跨页结果✓".repeat(30);
 const bytes = new TextEncoder().encode(JSON.stringify({content:output,tool_execution:{state:"completed"}}));
 const backend = new FakeBackend([{role:"assistant",content:"",toolCalls:[{id:"detached",name:"bash",arguments:"{}",resultObservation:{state:"completed",messageId:"outside",version:1,contentRef:{digest:"detached-digest",bytes:bytes.length,indexDigest:"",mediaType:"application/json"}}}]}]);
 backend.HistoryContentForTab = async (_,ref) => ({entryId:ref.entryId,field:ref.field,chunk:0,chunks:1,data:Array.from(bytes,b=>String.fromCharCode(b)).join(""),done:true,stale:false});
 const store = new TranscriptStore(backend,{windowMaxPages:1});
 const view = await store.loadLatest("detached-tab","/detached");
 const item=view?.items.find((item):item is Extract<Item,{kind:"tool"}>=>item.kind==="tool");
 if(!item) throw new Error("detached tool missing");
 eq(item.status,"done","cross-page completion is authoritative");
 eq(item.contentState,"unloaded","body is separately unloaded");
 const before=store.stats().residentWindowEntries;
 const loaded=JSON.parse((await store.requestToolContent("detached-tab",item,{}))!);
 eq(loaded.output,output,"detached UTF-8 output is complete");
 eq(store.stats().residentWindowEntries,before,"detached results do not grow the resident window");
 backend.HistoryContentForTab=async()=>{throw new Error("unreadable result")};
 let failed=false;try{await store.requestToolContent("detached-tab",item,{})}catch{failed=true}
 ok(failed,"unreadable content remains an error");
 eq(item.status,"done","read failure does not turn completion into cancellation");
}

await verifyTranscriptContentOwnership();
console.log(`\n${passed} passed, ${failed} failed; content ownership interleavings passed`);
if (failed > 0) process.exit(1);
