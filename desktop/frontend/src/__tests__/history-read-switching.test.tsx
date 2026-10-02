import assert from "node:assert/strict";
import { mock } from "node:test";
import { JSDOM } from "jsdom";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import type { ReasonixDesktopHost } from "../lib/desktopHost";
import type { SessionMeta } from "../lib/types";

const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage, IS_REACT_ACT_ENVIRONMENT: true });
type Request = { workspaceRoot: string; cursor: string };
const reads: { name: string; req: Request; resolve: (page: unknown) => void }[] = [];
const released: string[] = [];
window.reasonixDesktop = {
  kind: "electron", contract: { commands: ["ListHistorySessions", "SearchHistoryContent", "ReleaseReadSnapshot"] }, native: { window: {} },
  on: () => () => {},
  invoke: (name: string, args: unknown[]) => {
    if (name === "ReleaseReadSnapshot") { released.push(String(args[0])); return Promise.resolve(); }
    return new Promise(resolve => reads.push({ name, req: args[0] as Request, resolve }));
  },
} as unknown as ReasonixDesktopHost;
const { useHistoryCatalog } = await import("../lib/useHistoryCatalog");
let state!: ReturnType<typeof useHistoryCatalog>;
function Harness({ project, session, trash = false, sessionId }: { project: string; session: string; trash?: boolean; sessionId?: string }) {
  state = useHistoryCatalog({ isTrash: trash, suppliedSessions: [{ path: session, sessionId, workspaceRoot: project, current: true } as SessionMeta], scope: "project", status: "current", timeFilter: "all", query: "marker" });
  return <div>{state.searchHits.map(hit => hit.sessionPath).join(",")}</div>;
}
const root = createRoot(document.getElementById("root")!);
const render = async (project: string, session: string, trash = false, sessionId?: string) => { await act(async () => root.render(<Harness project={project} session={session} trash={trash} sessionId={sessionId} />)); };
const advance = async () => { await act(async () => mock.timers.tick(200)); };
async function finish(start: number, session: string, id: string) {
  await act(async () => {
    for (const read of reads.slice(start, start + 2)) {
      const search = read.name === "SearchHistoryContent";
      read.resolve({ items: Array.from({ length: 50 }, (_, i) => search ? { sessionPath: session, messageIndex: i, snippet: session } : { path: session, current: true }), nextCursor: `${id}-cursor`, snapshotId: `${id}-${search ? "hits" : "sessions"}`, revision: 1, status: { indexed: 1, total: 1 } });
    }
  });
}
mock.timers.enable({ apis: ["setTimeout", "Date"] });
try {
  await render("/A", "/A/1"); await advance(); await finish(0, "/A/1", "a1");
  assert.equal(state.sessions[0].path, "/A/1");
  const oldLoadMore = state.loadMore;
  await act(async () => state.loadMore());
  assert.equal(reads.length, 4);
  await render("/B", "/B/1");
  assert.equal(state.searchHits.length, 0, "A must disappear before B's debounce completes");
  assert.equal(state.nextCursor, "");
  await act(async () => { state.loadMore(); oldLoadMore(); });
  assert.equal(reads.length, 4, "neither old nor new callbacks may send A's cursor under B");
  await advance();
  assert.equal(reads[4].req.workspaceRoot, "/B");
  assert.equal(reads[4].req.cursor, "");
  await finish(4, "/B/1", "b1");
  await finish(2, "/A/late", "a1");
  assert.equal(state.sessions[0].path, "/B/1", "late A append cannot overwrite B");
  assert.ok(!released.includes("b1-hits"), "late A cannot release B's handle");

  await render("/B", "/B/2"); await advance();
  assert.equal(reads.length, 8, "same-project session switch starts a new generation");
  await render("/A", "/A/1"); await advance();
  await finish(8, "/A/1", "a2");
  await finish(6, "/B/2", "b2");
  assert.equal(state.sessions[0].path, "/A/1", "A -> B -> A rejects older B completion");
  assert.ok(released.includes("b2-hits"));
  assert.ok(released.includes("a1-hits"));
  assert.ok(released.includes("b1-hits"));
  await render("/A", "/A/1", true);
  await act(async () => state.loadMore()); await advance();
  assert.equal(reads.length, 10, "trash does not continue a history read");
  assert.equal(state.searchHits.length, 0);
  await render("/A", "", false, "canonical-a"); await advance(); await finish(10, "canonical-a", "ca");
  await render("/A", "", false, "canonical-b");
  assert.equal(state.searchHits.length, 0, "canonical session identity does not depend on a legacy path");
  await advance(); await finish(12, "canonical-b", "cb");
  assert.equal(reads.length, 14);
  assert.equal(state.sessions[0].path, "canonical-b");
  await act(async () => root.unmount());
  assert.ok(released.includes("a2-hits"));
} finally { mock.timers.reset(); }
console.log("PASS project/session A-B-A switching, debounce clicks, late responses, trash and snapshot ownership");
