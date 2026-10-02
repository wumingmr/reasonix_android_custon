import assert from "node:assert/strict";
import { loadProjectTreePageWindow } from "../lib/projectTreeWindow";

for (const size of [205, 405, 1000]) {
  const calls: string[] = [];
  let expired = false;
  const result = await loadProjectTreePageWindow("", size, async (cursor, limit) => {
    calls.push(cursor);
    if (cursor === "old:200") { expired = true; throw Object.assign(new Error("expired"), { code: "stale_cursor" }); }
    const id = expired ? "fresh" : "old";
    const offset = Number(cursor.split(":")[1] ?? 0);
    return { snapshotId: id, items: Array.from({ length: Math.min(limit, size - offset) }, (_, i) => offset + i), nextCursor: offset + limit < size ? `${id}:${offset + limit}` : "", revision: 1 };
  });
  assert.equal(result.replacedSnapshot, true);
  assert.equal(result.snapshotId, "fresh");
  assert.deepEqual(result.items, Array.from({ length: size }, (_, i) => i));
  assert.equal(calls.filter((cursor) => cursor === "").length, 2);
}

let attempts = 0;
await assert.rejects(loadProjectTreePageWindow("", 405, async (cursor) => {
  if (!cursor) { attempts++; return { items: [1], nextCursor: "next", revision: 1 }; }
  throw Object.assign(new Error("expired"), { code: "stale_cursor" });
}), /expired/);
assert.equal(attempts, 2, "one recovery per logical operation");

const replacement = await loadProjectTreePageWindow("expired:200", 5, async (cursor, limit) => {
  if (cursor.startsWith("expired")) throw new Error("session_operation:stale_cursor:expired");
  const offset = Number(cursor || 0);
  return { items: Array.from({ length: limit }, (_, i) => offset + i), nextCursor: String(offset + limit), revision: 2, snapshotId: "replacement" };
}, 205);
assert.equal(replacement.items.length, 205, "append recovery restores the entire visible window");
assert.equal(replacement.replacedSnapshot, true);

await assert.rejects(loadProjectTreePageWindow("next", 5, async () => ({ items: [1], nextCursor: "next", revision: 1 })), /did not advance/);
await assert.rejects(loadProjectTreePageWindow("", 205, async (cursor) => ({ items: Array(200).fill(1), nextCursor: "next", snapshotId: cursor ? "b" : "a", revision: 1 })), /Mixed read snapshots/);
console.log("PASS immutable windows, internal-page expiry, bounded recovery and whole-window replacement");
