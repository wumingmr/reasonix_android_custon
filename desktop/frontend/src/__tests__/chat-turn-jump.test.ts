import assert from "node:assert/strict";
import { ChatMountedOrder } from "../lib/chatMountedOrder";
import { ChatTurnJump, type TurnJumpDeps } from "../lib/chatTurnJump";
import type { ChatScrollController } from "../lib/chatScrollController";
import type { NavigateToTurn, TurnNavigationOutcome } from "../lib/historyTurnNavigation";
import { findLoadedTurn, indexLoadedTurns } from "../lib/chatTurnRail";

function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>(done => { resolve = done; }); return { promise, resolve }; }
function setup(navigate: NavigateToTurn, clock?: TurnJumpDeps["clock"]) {
  const mounts = new ChatMountedOrder(); const mounted = new Set<string>(); const writes: string[] = []; const readers = new Set<() => void>();
  const scroll = { stopFollowing() {}, jump(key: string) { writes.push(key); }, subscribeReaderIntent(fn: () => void) { readers.add(fn); return () => { readers.delete(fn); }; } } as unknown as ChatScrollController;
  let refreshes = 0;
  const jump = new ChatTurnJump({ mounts, scroll, resolveKey: target => mounted.has(target.messageId) ? target.messageId : undefined,
    navigate, clock, isCurrent: () => true, refresh: async () => { refreshes++; } });
  return { jump, mounts, mounted, writes, refreshes: () => refreshes, read: () => { for (const fn of [...readers]) fn(); } };
}
const target = (id: string) => ({ turn: id, resolve: async () => ({ messageId: id, generation: "g", snapshotSequence: 1 }) });
{
  let reads = 0;
  const h = setup(async t => { reads++; h.mounted.add(t.messageId); return "loaded"; });
  await h.jump.jump(target("oldest"));
  assert.equal(reads, 1, "far jump reads one target window");
  assert.deepEqual(h.writes, ["oldest"]); h.jump.dispose();
}
{
  const request = deferred<TurnNavigationOutcome>(); let valid!: () => boolean;
  const h = setup(async (_target, current) => { valid = current; return request.promise; });
  const old = h.jump.jump(target("old")); await Promise.resolve();
  h.mounted.add("new"); h.jump.jumpTo("new");
  assert.equal(valid(), false); request.resolve("loaded"); await old;
  assert.deepEqual(h.writes, ["new"]); h.jump.dispose();
}
{
  const request = deferred<TurnNavigationOutcome>(); let valid!: () => boolean;
  const h = setup(async (_target, current) => { valid = current; return request.promise; });
  const run = h.jump.jump(target("old")); await Promise.resolve(); h.read();
  assert.equal(valid(), false); request.resolve("loaded"); await run;
  assert.deepEqual(h.writes, []); h.jump.dispose();
}
{
  const identities: string[] = [];
  const h = setup(async t => { identities.push(t.messageId); if (identities.length === 1) return "stale"; h.mounted.add(t.messageId); return "loaded"; });
  await h.jump.jump(target("same-message"));
  assert.deepEqual(identities, ["same-message", "same-message"]); assert.equal(h.refreshes(), 1); h.jump.dispose();
}
{
  const h = setup(async () => "stale"); await h.jump.jump(target("gone"));
  assert.equal(h.refreshes(), 1); assert.equal(h.jump.getSnapshot().reason, "snapshotExpired"); h.jump.dispose();
}
{
  const gate = deferred<TurnNavigationOutcome>(); const h = setup(() => gate.promise);
  const run = h.jump.jump(target("later-mount")); await Promise.resolve(); gate.resolve("loaded"); await Promise.resolve(); await Promise.resolve();
  assert.deepEqual(h.writes, []); h.mounted.add("later-mount"); h.mounts.publish(["later-mount"]); await run;
  assert.deepEqual(h.writes, ["later-mount"]); h.jump.dispose();
}
const loaded = indexLoadedTurns(["optimistic"], () => ({ id: "optimistic", messageId: "stable" }));
{
  let timeout!: () => void;
  const h = setup(async () => "loaded", {
    setTimeout: ((fn: () => void) => { timeout = fn; return 1; }) as unknown as typeof setTimeout,
    clearTimeout: (() => {}) as typeof clearTimeout,
  });
  const run = h.jump.jump(target("never-mounted"));
  await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
  timeout(); await run;
  assert.equal(h.jump.getSnapshot().reason, "mountTimeout");
  assert.deepEqual(h.writes, []); h.jump.dispose();
}
{
  const selected: string[] = [];
  const h = setup(async t => { selected.push(t.messageId); return "unavailable"; });
  let id = "original";
  await h.jump.jump({ turn: "pending:1", resolve: async () => ({ messageId: id }) });
  id = "replacement";
  await h.jump.retry();
  assert.deepEqual(selected, ["original", "original"], "retry never resolves an ordinal to a different message");
  h.jump.dispose();
}
assert.equal(findLoadedTurn(loaded, { id: "m:stable", messageId: "stable", turn: 1, order: 0, prompt: "" }), "optimistic");
console.log("turn jump: direct read, identity, latest intent, native takeover and mount confirmation passed");
