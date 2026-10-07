import assert from "node:assert/strict";
import test from "node:test";
import { ChatSource } from "../lib/chatViewSource";
import { TranscriptStore } from "../lib/transcriptStore";
import type { HistoryEntry } from "../lib/types";
import { FakeBackend } from "./helpers/transcriptFakeBackend";

const call = (id: string, name: string, parentId?: string) => ({ id, name, arguments: "{}", parentId });
const assistant = (n: number, messageId: string, toolCalls: ReturnType<typeof call>[]): HistoryEntry => ({
  entryId: `m:${messageId}`, turn: 1, order: n, refs: [], message: { role: "assistant", messageId, content: "", toolCalls },
});
const result = (n: number, toolCallId: string, toolName: string): HistoryEntry => ({
  entryId: `tool:${toolCallId}`, turn: 1, order: n, refs: [], message: { role: "tool", toolCallId, toolName, content: "ok" },
});

function project(entries: HistoryEntry[]) {
  const user: HistoryEntry = { entryId: "m:u", turn: 1, order: 0, refs: [], message: { role: "user", messageId: "u", content: "go" } };
  const store = new TranscriptStore(new FakeBackend([]));
  return store.installSlice("subagent-parent", "/subagent-parent", {
    entries: [user, ...entries.map((entry, index) => ({ ...entry, order: index + 1 }))],
    nextCursor: "", newerCursor: "", hasOlder: false, hasNewer: false, totalTurns: 1, startTurn: 1, endTurn: 1,
    revision: 1, revisionKnown: true, digest: "subagent-parent", stale: false,
  });
}

function topLevelTools(items: ReturnType<typeof project>["items"]): string[] {
  const source = new ChatSource("subagent-parent-view");
  source.update({ items, localSubmissions: [], running: false, hydrating: false, hasOlder: false, loadingOlder: false });
  const names = source.getOrderSnapshot().flatMap(key => {
    const node = source.getNodeSnapshot(key);
    return node?.kind === "tool" ? [node.item.id] : [];
  });
  source.dispose();
  return names;
}

test("a task sub-agent's own calls keep their parent through the record projection", () => {
  const projection = project([
    assistant(1, "parent", [call("task-1", "use_capability")]),
    assistant(2, "child", [call("task-1/ls1", "ls", "task-1")]),
    result(3, "task-1/ls1", "ls"),
    result(4, "task-1", "use_capability"),
  ]);
  const child = projection.items.find(item => item.kind === "tool" && item.id === "task-1/ls1");
  assert.equal(child?.kind === "tool" && child.parentId, "task-1");
  assert.deepEqual(topLevelTools(projection.items), ["task-1"]);
});

test("a fleet worker's calls nest two levels under the group", () => {
  const projection = project([
    assistant(1, "parent", [call("fl", "use_capability")]),
    assistant(2, "group", [call("fl/fleet-1", "task", "fl")]),
    assistant(3, "worker", [call("fl/fleet-1/read1", "read_file", "fl/fleet-1")]),
    result(4, "fl/fleet-1/read1", "read_file"),
    result(5, "fl/fleet-1", "task"),
    result(6, "fl", "use_capability"),
  ]);
  const parents = new Map(projection.items.flatMap(item => item.kind === "tool" ? [[item.id, item.parentId]] : []));
  assert.equal(parents.get("fl/fleet-1"), "fl");
  assert.equal(parents.get("fl/fleet-1/read1"), "fl/fleet-1");
  assert.deepEqual(topLevelTools(projection.items), ["fl"]);
});
