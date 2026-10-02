import assert from "node:assert/strict";
import { ChatSource, type ChatInput } from "../lib/chatViewSource";
import type { Item } from "../lib/useController";

const tool = (id: string): Item => ({ kind: "tool", id, name: "read_file", args: "", readOnly: true, status: "done", output: "ok" });
const answer = (id: string, streaming = false): Item => ({ kind: "assistant", id, text: "done", reasoning: "", streaming });
const base: ChatInput = { items: [], running: false, hydrating: false, hasOlder: true, loadingOlder: false };
const process = (source: ChatSource, key: string) => {
  const node = source.getNodeSnapshot(`${key}:process`);
  assert.equal(node?.kind, "process");
  return node as Extract<NonNullable<typeof node>, { kind: "process" }>;
};

const head: Item[] = [tool("t1"), tool("t2"), answer("a1")];
const settled = new ChatSource("head-settled");
settled.update({ ...base, items: head });
await Promise.resolve();
assert.ok(process(settled, "history-head").foldable, "a head group with tools can fold");
assert.ok(process(settled, "history-head").collapsed, "a settled head group starts folded");
settled.toggleProcess("history-head");
assert.equal(process(settled, "history-head").collapsed, false, "the head group folds and unfolds by hand");
settled.toggleProcess("history-head");
settled.dispose();

const running = new ChatSource("head-running");
running.update({ ...base, items: [tool("t1"), answer("a1", true)], running: true });
await Promise.resolve();
const live = process(running, "history-head");
assert.ok(!live.foldable && !live.collapsed, "a head group still running stays open");
running.dispose();

const chosen = new ChatSource("head-choice");
chosen.update({ ...base, items: head });
await Promise.resolve();
chosen.toggleProcess("history-head");
const user: Item = { kind: "user", id: "u0", text: "long task", checkpointTurn: 1 };
chosen.update({ ...base, items: [user, ...head], hasOlder: false });
await Promise.resolve();
assert.equal(chosen.getNodeSnapshot("history-head:process"), undefined, "the head key is gone once the user message arrives");
assert.equal(process(chosen, "u0").collapsed, false, "the manual choice follows the turn when its user message loads");
chosen.dispose();

const folded = new ChatSource("head-folded");
folded.update({ ...base, items: head });
await Promise.resolve();
folded.update({ ...base, items: [user, ...head], hasOlder: false });
await Promise.resolve();
assert.ok(process(folded, "u0").collapsed, "an untouched head stays folded after the user message loads");
folded.dispose();

const unrelated = new ChatSource("head-unrelated");
unrelated.update({ ...base, items: head });
await Promise.resolve();
unrelated.toggleProcess("history-head");
const other: Item[] = [{ kind: "user", id: "u9", text: "other", checkpointTurn: 9 }, tool("x1"), answer("ax")];
unrelated.update({ ...base, items: other });
await Promise.resolve();
assert.ok(process(unrelated, "u9").collapsed, "a choice made on the head does not leak onto a turn that does not contain it");
unrelated.dispose();

console.log("chat view: history head fold, running head and choice migration passed");
