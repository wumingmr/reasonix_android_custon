// Run: npx tsx src/__tests__/project-tree-window.test.ts
import assert from "node:assert/strict";
import {
  PROJECT_TREE_WINDOW_INITIAL,
  PROJECT_TREE_WINDOW_STEP,
  createProjectTreeRequestLimiter,
  forgetProjectTreeWindowLimits,
  loadProjectTreePageWindow,
  projectTreeListKey,
  projectTreeKnownGroupIDs,
  projectTreeProjectsNeedingInitialLoad,
  projectTreeRuntimeWindowLimits,
  projectTreeWindowProjection,
  projectTreeWindowRows,
  reloadProjectTreeTopicLists,
  rememberProjectTreeWindowLimit,
  resetProjectTreeRuntimeWindowLimits,
} from "../lib/projectTreeWindow";
import type { ProjectNode } from "../lib/types";

function topics(count: number): ProjectNode[] {
  return Array.from({ length: count }, (_, index) => ({
    key: `topic_${index + 1}`,
    kind: "topic",
    label: `Topic ${index + 1}`,
    topicId: `topic-${index + 1}`,
    children: [],
  }));
}

for (const count of [0, 1, 5, 6, 15, 16]) {
  const rows = topics(count);
  assert.equal(
    projectTreeWindowRows(rows, PROJECT_TREE_WINDOW_INITIAL, () => false).length,
    Math.min(count, PROJECT_TREE_WINDOW_INITIAL),
    `default window for ${count}`,
  );
}

const many = topics(30);
assert.deepEqual(
  [
    PROJECT_TREE_WINDOW_INITIAL,
    PROJECT_TREE_WINDOW_INITIAL + PROJECT_TREE_WINDOW_STEP,
    PROJECT_TREE_WINDOW_INITIAL + PROJECT_TREE_WINDOW_STEP * 2,
  ].map((limit) => projectTreeWindowRows(many, limit, () => false).length),
  [5, 10, 15],
);

const activeOutside = projectTreeWindowProjection(many, 5, (node) => node.topicId === "topic-30");
assert.equal(activeOutside.rows.length, 6);
assert.equal(activeOutside.rows[activeOutside.rows.length - 1]?.topicId, "topic-30");
assert.equal(new Set(activeOutside.rows.map((node) => node.key)).size, activeOutside.rows.length);
assert.equal(activeOutside.hasHiddenLoadedRows, true);

const activeOnlyOverflow = projectTreeWindowProjection(topics(6), 5, (node) => node.topicId === "topic-6");
assert.equal(activeOnlyOverflow.rows.length, 6);
assert.equal(activeOnlyOverflow.hasHiddenLoadedRows, false, "an active fallback row alone does not imply hidden loaded rows");

const activeInside = projectTreeWindowRows(many, 5, (node) => node.topicId === "topic-3");
assert.equal(activeInside.length, 5);
assert.equal(activeInside.filter((node) => node.topicId === "topic-3").length, 1);

assert.notEqual(projectTreeListKey("project", "group-a"), projectTreeListKey("project", "group-b"));
assert.notEqual(projectTreeListKey("project", "", "needle"), projectTreeListKey("project", ""));
assert.equal(projectTreeListKey("project", "", "  Needle "), projectTreeListKey("project", "", "needle"));

const knownGroupStates = {
  [projectTreeListKey("project", "bugs")]: { loading: false, initialized: true },
  [projectTreeListKey("project", "feature")]: { loading: false, initialized: true },
  [projectTreeListKey("project", "", "needle")]: { loading: false, initialized: true },
  [projectTreeListKey("other", "ignored")]: { loading: false, initialized: true },
};
assert.deepEqual(projectTreeKnownGroupIDs(knownGroupStates, "project"), ["bugs", "feature"]);

const cachedProjects = [
  { key: "project-a", kind: "project", label: "A", children: [] },
  { key: "project-b", kind: "project", label: "B", children: [] },
] satisfies ProjectNode[];
const cachedPageStates = {
  [projectTreeListKey("project-a")]: { loading: false, initialized: true, itemKeys: ["a-1", "a-2"] },
  [projectTreeListKey("project-b")]: { loading: false, initialized: true, itemKeys: ["b-1", "b-2"] },
};
const scheduledLoads: string[] = [];
const scheduleInitialLoads = (expandedKeys: ReadonlySet<string>, pageStates = cachedPageStates) => {
  for (const project of projectTreeProjectsNeedingInitialLoad(cachedProjects, expandedKeys, "", pageStates, (item) => item.key)) {
    scheduledLoads.push(project.key);
  }
};
scheduleInitialLoads(new Set(["project-b"]));
assert.deepEqual(scheduledLoads, [], "collapsing one project does not reload an initialized sibling");
scheduleInitialLoads(new Set(["project-a", "project-b"]));
assert.deepEqual(scheduledLoads, [], "reopening one project or restoring all projects reuses initialized pages");
scheduleInitialLoads(new Set(["project-a", "project-b"]), {
  ...cachedPageStates,
  [projectTreeListKey("project-a")]: { ...cachedPageStates[projectTreeListKey("project-a")], initialized: false },
});
assert.deepEqual(scheduledLoads, ["project-a"], "only an explicitly invalidated expanded project requests its first page");

const reloadCalls: string[] = [];
const reloadProject = { key: "project", kind: "project", label: "Project", children: [] } satisfies ProjectNode;
await reloadProjectTreeTopicLists(reloadProject, "", knownGroupStates, async (_project, groupID) => {
  reloadCalls.push(groupID || "ungrouped");
});
assert.deepEqual(reloadCalls, ["ungrouped", "bugs", "feature"], "ordinary refresh replenishes every known group list");
reloadCalls.length = 0;
await reloadProjectTreeTopicLists(reloadProject, "needle", knownGroupStates, async (_project, groupID) => {
  reloadCalls.push(groupID || "search");
});
assert.deepEqual(reloadCalls, ["search"], "search refresh reloads only the project search list");

const limiter = createProjectTreeRequestLimiter(4);
assert.deepEqual(limiter.stats(), { active: 0, queued: 0 });
let running = 0;
let peak = 0;
const releases: Array<() => void> = [];
const tasks = Array.from({ length: 8 }, () => limiter.run(() => new Promise<void>((resolve) => {
  running += 1;
  peak = Math.max(peak, running);
  releases.push(() => {
    running -= 1;
    resolve();
  });
})));
await Promise.resolve();
assert.equal(peak, 4, "sidebar pagination is capped at four concurrent requests");
assert.equal(limiter.stats().active, 4, "diagnostic snapshot reports active list requests");
assert.equal(limiter.stats().queued, 4, "diagnostic snapshot reports queued list requests");
while (releases.length > 0) {
  releases.shift()?.();
  await Promise.resolve();
}
await Promise.all(tasks);
assert.deepEqual(limiter.stats(), { active: 0, queued: 0 }, "request limiter drains after all pages finish");

const pageCalls: Array<{ cursor: string; limit: number }> = [];
const pagedTopics = topics(250);
const restored = await loadProjectTreePageWindow("", 205, async (cursor, limit) => {
  pageCalls.push({ cursor, limit });
  const start = cursor ? Number(cursor) : 0;
  const end = Math.min(start + limit, 250);
  return {
    items: pagedTopics.slice(start, end),
    nextCursor: end < 250 ? String(end) : undefined,
    revision: pageCalls.length,
    complete: true,
  };
});
assert.deepEqual(pageCalls, [{ cursor: "", limit: 200 }, { cursor: "200", limit: 5 }]);
assert.equal(restored.items.length, 205, "refresh refills the complete expanded quota");
assert.equal(restored.nextCursor, "205");
assert.equal(restored.revision, 2);

const rememberedKey = projectTreeListKey("remembered-project", "feature");
const siblingKey = projectTreeListKey("sibling-project", "feature");
rememberProjectTreeWindowLimit(rememberedKey, 15);
rememberProjectTreeWindowLimit(siblingKey, 10);
assert.equal(projectTreeRuntimeWindowLimits()[rememberedKey], 15, "expanded quota survives a component remount");
resetProjectTreeRuntimeWindowLimits("remembered-project");
assert.equal(projectTreeRuntimeWindowLimits()[rememberedKey], undefined, "collapsing one project resets all of its window quotas");
assert.equal(projectTreeRuntimeWindowLimits()[siblingKey], 10, "collapsing one project preserves sibling quotas");
resetProjectTreeRuntimeWindowLimits();
assert.deepEqual(projectTreeRuntimeWindowLimits(), {}, "collapsing the whole tree resets every window quota");
rememberProjectTreeWindowLimit(rememberedKey, 15);
forgetProjectTreeWindowLimits(new Set(["other-project"]));
assert.equal(projectTreeRuntimeWindowLimits()[rememberedKey], undefined, "deleted projects release runtime quota state");

console.log("  PASS  project tree window contract");
