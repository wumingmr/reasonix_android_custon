import assert from "node:assert/strict";
import { test } from "node:test";
import { splitPinnedProjectTree } from "../lib/projectTreePresentation";
import { createProjectTreeRuntimeProjection } from "../lib/projectTreeRuntime";
import {
  projectTreeReadActivityKey,
  projectTreeSeedReadActivity,
  projectTreeTopicHasUnreadActivity,
  projectTreeTopicOpenRequest,
  projectTreeWithoutTopics,
  topicIsActive,
} from "../lib/projectTreeTopic";
import type { ProjectNode, ProjectRuntimeTopic } from "../lib/types";

const root = "/repo";
const topicId = "shared-topic";
const sessionA: ProjectNode = {
  key: "session-a", kind: "topic", label: "Original", root, topicId,
  session: { hostId: "local", sessionId: "a" }, sessionPath: "/sessions/a.jsonl",
  resultSequence: 100, turnsState: "ready", children: [],
};
const sessionB: ProjectNode = {
  key: "session-b", kind: "topic", label: "Fork", root, topicId,
  session: { hostId: "local", sessionId: "b" }, sessionPath: "/sessions/b.jsonl",
  resultSequence: 10, turnsState: "ready", children: [],
};
const catalog: ProjectNode[] = [{
  key: "project", kind: "project", label: "Project", root,
  children: [sessionA, sessionB],
}];
const runtimeA: ProjectRuntimeTopic = {
  scope: "project", workspaceRoot: root,
  node: { ...sessionA, label: "Runtime label", running: true, open: true, status: "waiting_confirmation" },
};

test("an empty runtime snapshot preserves two durable sessions sharing a topic", () => {
  const projected = createProjectTreeRuntimeProjection().apply(catalog, []);
  assert.deepEqual(projected[0]?.children?.map(row => row.session?.sessionId), ["a", "b"]);
  assert.equal(projected[0]?.children?.[0]?.sessionPath, "/sessions/a.jsonl");
  assert.equal(projected[0]?.children?.[1]?.sessionPath, "/sessions/b.jsonl");
});

test("one runtime affects only its exact session and never changes a sibling open target", () => {
  const projection = createProjectTreeRuntimeProjection();
  const overlaid = projection.apply(catalog, [runtimeA]);
  const [a, b] = overlaid[0]?.children ?? [];
  assert.equal(a?.status, "waiting_confirmation");
  assert.equal(a?.running, true);
  assert.equal(a?.sessionPath, "/sessions/a.jsonl");
  assert.equal(b?.status, undefined);
  assert.equal(b?.running, undefined);
  assert.equal(b?.sessionPath, "/sessions/b.jsonl");
  assert.equal(projectTreeTopicOpenRequest(a!)?.sessionPath, "session-id:a");
  assert.equal(projectTreeTopicOpenRequest(b!)?.sessionPath, "session-id:b");
  assert.deepEqual(projection.apply(overlaid, [runtimeA])[0]?.children?.map(row => row.session?.sessionId), ["a", "b"]);
  assert.deepEqual(projection.apply(overlaid, [])[0]?.children?.map(row => row.session?.sessionId), ["a", "b"]);
});

test("two running sessions sharing a topic retain their own runtime status", () => {
  const runtimeB: ProjectRuntimeTopic = {
    scope: "project", workspaceRoot: root,
    node: { ...sessionB, running: true, status: "thinking" },
  };
  const projected = createProjectTreeRuntimeProjection().apply(catalog, [runtimeA, runtimeB]);
  assert.deepEqual(projected[0]?.children?.map(row => [row.session?.sessionId, row.status]), [
    ["a", "waiting_confirmation"], ["b", "thinking"],
  ]);
  const afterAStops = createProjectTreeRuntimeProjection().apply(catalog, [runtimeB]);
  assert.deepEqual(afterAStops[0]?.children?.map(row => [row.session?.sessionId, row.status]), [
    ["a", undefined], ["b", "thinking"],
  ]);
});

test("runtime reconciliation preserves dormant durable branch children", () => {
  const branchA: ProjectNode = { ...sessionA, kind: "session" };
  const branchB: ProjectNode = { ...sessionB, kind: "session" };
  const parent: ProjectNode = { key: "legacy-parent", kind: "topic", label: "Legacy", root, topicId,
    children: [branchA, branchB] };
  const tree: ProjectNode[] = [{ ...catalog[0]!, children: [parent] }];
  const projection = createProjectTreeRuntimeProjection();
  const withRuntime = projection.apply(tree, [{ ...runtimeA, node: { ...parent,
    children: [{ ...branchA, status: "waiting_confirmation", running: true }] } }]);
  assert.deepEqual(withRuntime[0]?.children?.[0]?.children?.map(row => row.session?.sessionId), ["a", "b"]);
  assert.equal(withRuntime[0]?.children?.[0]?.children?.[1]?.running, undefined);
  const stopped = projection.apply(withRuntime, []);
  assert.deepEqual(stopped[0]?.children?.[0]?.children?.map(row => row.session?.sessionId), ["a", "b"]);
});

test("pinning A neither hides B nor paints a duplicate of A", () => {
  const pinnedTree: ProjectNode[] = [{ ...catalog[0]!, children: [{ ...sessionA, pinned: true }, sessionB] }];
  const sections = splitPinnedProjectTree(pinnedTree, "updated");
  assert.deepEqual(sections.pinned.map(row => row.session?.sessionId), ["a"]);
  assert.deepEqual(sections.projects[0]?.children?.map(row => row.session?.sessionId), ["b"]);
});

test("the active A route cannot mark B active or suppress B's unread result", () => {
  const bKey = projectTreeReadActivityKey(sessionB)!;
  assert.equal(topicIsActive(sessionA, "project", root, topicId, "session-id:a"), true);
  assert.equal(topicIsActive(sessionB, "project", root, topicId, "session-id:a"), false);
  assert.equal(projectTreeTopicHasUnreadActivity(
    { ...sessionB, resultSequence: 20 }, { [bKey]: 10 }, "project", root, topicId, "session-id:a",
  ), true);
});

test("ordinary ready metadata cannot repair an imported baseline without authority", () => {
  const bKey = projectTreeReadActivityKey(sessionB)!;
  const seeded = projectTreeSeedReadActivity(catalog, { [bKey]: 100 });
  assert.equal(seeded[bKey], 100);
  assert.equal(projectTreeTopicHasUnreadActivity(
    { ...sessionB, resultSequence: 20 }, seeded, "project", root, "unrelated", "session-id:a",
  ), false);
});

test("a committed archive tombstone for B cannot hide A or revive B from a stale page", () => {
  const filtered = projectTreeWithoutTopics(catalog, new Set(["ref\u0000local\u0000b"]));
  assert.deepEqual(filtered[0]?.children?.map(row => row.session?.sessionId), ["a"]);
  const staleIncoming: ProjectNode[] = [{ ...catalog[0]!, children: [sessionA, sessionB] }];
  const fenced = projectTreeWithoutTopics(staleIncoming, new Set(["ref\u0000local\u0000b"]));
  assert.deepEqual(fenced[0]?.children?.map(row => row.session?.sessionId), ["a"]);
});
