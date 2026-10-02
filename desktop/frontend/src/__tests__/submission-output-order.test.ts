import assert from "node:assert/strict";
import test from "node:test";
import { initialState, reducer, type Item, type State } from "../lib/useController";

const user = (id: string, turnId = "turn"): Item => ({ kind: "user", id, messageId: id, turnId, text: "question" });
const answer = (id: string): Item => ({ kind: "assistant", id, turnId: "turn", text: id, reasoning: "", streaming: false });
const tool = (id: string): Extract<Item, { kind: "tool" }> => ({ kind: "tool", id, turnId: "turn", name: "read_file", args: "{}", readOnly: true, status: "done", output: "contents" });
const install = (state: State, items: Item[]) => reducer(state, {
  type: "transcript_records", confirmedUsers: [], projection: {
    items, removeIds: [], startTurn: 0, endTurn: 1, totalTurns: 1,
    hasOlder: false, hasNewer: false, revision: 1, revisionKnown: true, digest: "cut",
  },
});
const stream = (state: State, id: string) => reducer(state, {
  type: "event", e: { kind: "text", messageId: id, turnId: "turn", text: "next answer" },
});
const ids = (state: State) => state.items.map(item => item.id);
const empty = (): State => ({ ...initialState, transcriptProtocol: 2, activeTurnId: "turn" });

test("a later sampling answer stays after the earlier answer and tool on repeated projection patches", () => {
  const formal = [user("user"), answer("first"), tool("call")];
  let state = stream(install(empty(), formal), "next");
  const liveAnswer = state.items[state.items.length - 1];
  for (let revision = 0; revision < 3; revision++) {
    state = install(state, [...formal.slice(0, -1), { ...tool("call"), output: `contents ${revision}` }]);
    assert.deepEqual(ids(state), ["user", "first", "call", "m:next"]);
    assert.equal(state.items[state.items.length - 1], liveAnswer, "patching an earlier tool must retain the live host");
  }
});

test("a newly formal predecessor preserves the order of remaining live tools and answers", () => {
  const formal = [user("user"), answer("first"), tool("call")];
  let state = stream(install(empty(), formal), "second");
  state = reducer(state, { type: "event", e: { kind: "tool_dispatch", turnId: "turn", messageId: "second",
    tool: { id: "second-call", name: "read_file", args: "{}", readOnly: true } } });
  state = stream(state, "third");
  const expected = ["user", "first", "call", "m:second", "second-call", "m:third"];
  assert.deepEqual(ids(state), expected);
  state = install(state, [...formal, answer("m:second")]);
  assert.deepEqual(ids(state), expected);
  state = install(state, [...formal, answer("m:second"), tool("second-call")]);
  assert.deepEqual(ids(state), expected);
});

test("a delayed user stays before its formal tool and live answer without reversing the output", () => {
  let state = stream(install(empty(), [user("previous", "previous-turn"), tool("call")]), "next");
  state = install(state, [user("previous", "previous-turn"), tool("call"), user("user")]);
  assert.deepEqual(ids(state), ["previous", "user", "call", "m:next"]);
});

test("reclaiming a formal predecessor preserves the surviving predecessor of a live row", () => {
  let state = stream(install(empty(), [user("user"), answer("first"), tool("call")]), "next");
  state = install(state, [user("user"), answer("first")]);
  assert.deepEqual(ids(state), ["user", "first", "m:next"]);
});

for (const mode of ["event", "stream_batch"] as const) test(`${mode} preserves the reader window and the complete offscreen stream`, () => {
  let state: State = { ...empty(), historyHasNewer: true, historyHasOlder: true,
    items: [user("old-reader", "old-turn")] };
  const resident = state.items;
  for (const text of ["first", "second"]) {
    state = reducer(state, mode === "event"
      ? { type: "event", e: { kind: "text", messageId: "active", turnId: "turn", text } }
      : { type: "stream_batch", segments: [{ kind: "text", delta: text }] });
    assert.equal(state.historyHasNewer, true);
    assert.equal(state.items, resident);
    assert.equal(state.offscreenItems?.length, 1);
    assert.equal(state.offscreenItems[0].turnId, "turn");
  }
  assert.equal(state.live?.text, "firstsecond");
  const liveHost = state.offscreenItems![0];
  state = reducer(state, { type: "history_append", items: [], startTurn: 0, endTurn: 1, totalTurns: 1, hasOlder: true, hasNewer: false });
  assert.equal(state.items.find(item => item.id === liveHost.id), liveHost);
  assert.equal(state.offscreenItems, undefined);
  assert.equal(state.live?.text, "firstsecond");
});
