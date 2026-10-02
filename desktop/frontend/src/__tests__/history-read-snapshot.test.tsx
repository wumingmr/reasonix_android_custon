import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import type { ReasonixDesktopHost } from "../lib/desktopHost";

const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage, IS_REACT_ACT_ENVIRONMENT: true });
const listeners = new Map<string, (...args: unknown[]) => void>();
let calls = 0;
let expire = false;
let fail = false;
let held: (() => void) | undefined;
const releases: string[] = [];
window.reasonixDesktop = {
  kind: "electron", contract: { commands: ["ListHistorySessions", "SearchHistoryContent", "ReleaseReadSnapshot"] },
  native: { window: {} },
  on: (name: string, cb: (...args: unknown[]) => void) => { listeners.set(name, cb); return () => { listeners.delete(name); }; },
  invoke: async (name: string, args: unknown[]) => {
    if (name === "ReleaseReadSnapshot") { releases.push(String(args[0])); return; }
    if (name === "ListHistorySessions") return { items: [], nextCursor: "", revision: 1, partial: false, staleCursor: false, snapshotId: "sessions" };
    calls++;
    const req = args[0] as { cursor: string; limit: number };
    if (held) await new Promise<void>((resolve) => { held = resolve; });
    if (fail) throw new Error("disk failed");
    if (expire && req.cursor) { expire = false; return { items: [], staleCursor: true }; }
    const offset = Number(req.cursor || 0);
    const end = Math.min(105, offset + req.limit);
    return { items: Array.from({ length: end - offset }, (_, i) => ({ sessionPath: "/fixture", messageIndex: offset + i, partIndex: 0, snippet: String(offset + i) })), nextCursor: end < 105 ? String(end) : "", snapshotId: "search", revision: calls, partial: false, staleCursor: false, status: { indexed: 1, total: 1 } };
  },
} as unknown as ReasonixDesktopHost;
const { useHistoryCatalog } = await import("../lib/useHistoryCatalog");
let state!: ReturnType<typeof useHistoryCatalog>;
function Harness() { state = useHistoryCatalog({ isTrash: false, suppliedSessions: [], scope: "all", status: "all", timeFilter: "all", query: "marker" }); return <div>{state.searchHits.length}:{state.error}</div>; }
const root = createRoot(document.getElementById("root")!);
await act(async () => { root.render(<Harness />); });
await act(async () => { await new Promise((resolve) => setTimeout(resolve, 240)); });
assert.equal(state.searchHits.length, 50, "body-only result remains visible");
expire = true;
await act(async () => { state.loadMore(); });
assert.equal(state.searchHits.length, 100, "paired expiry rebuild preserves expanded size");
assert.equal(new Set(state.searchHits.map((hit) => hit.messageIndex)).size, 100);
fail = true;
await act(async () => { state.loadMore(); });
assert.equal(state.searchHits.length, 100, "failed append retains rendered rows");
assert.equal(state.error, "disk failed");
fail = false;
held = () => {};
await act(async () => { state.loadMore(); });
const before = calls;
await act(async () => {
  for (let i = 0; i < 20; i++) listeners.get("history-index:changed-v1")?.({ revision: i });
  state.loadMore();
});
assert.equal(calls, before, "continuous updates and duplicate load-more do not overlap an active read");
await act(async () => { const finish = held; held = undefined; finish?.(); });
assert.equal(state.searchHits.length, 105);
await act(async () => { root.unmount(); });
assert.ok(releases.includes("search"));
console.log("PASS paired history recovery, retained failure state, coalesced updates and snapshot release");
