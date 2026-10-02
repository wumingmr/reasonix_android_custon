import assert from "node:assert/strict";
import { test } from "node:test";
import { projectSessionIdentity, projectSessionRowKey } from "../lib/projectSessionIdentity";
import { createProjectTreeRuntimeProjection } from "../lib/projectTreeRuntime";
import { buildSidebarRegionProps } from "../app-shell/chromeRegionBuilders";
import {
  projectTreeReadActivityKey,
  projectTreeSeedReadActivity,
  projectTreeTopicHasUnreadActivity,
  projectTreeWithoutTopics,
  topicIsActive,
  mergeProjectTopicPage,
  mergeIncompleteProjectTopicPage,
} from "../lib/projectTreeTopic";
import type { ProjectNode, ProjectRuntimeTopic } from "../lib/types";

const root = "/repo";
const topicId = "shared-topic";

function legacySession(id: string, activity = 10): ProjectNode {
  return {
    key: `legacy-${id}`, kind: "topic", label: `Legacy ${id}`, root, topicId,
    sessionPath: `/legacy/${id}.jsonl`, lastActivityAt: activity, children: [],
  };
}

function canonicalSession(id: string, sequence = 10): ProjectNode {
  return {
    ...legacySession(id), key: `session-${id}`,
    session: { hostId: "local", sessionId: id },
    resultSequence: sequence, turnsState: "ready",
  };
}

function catalog(...children: ProjectNode[]): ProjectNode[] {
  return [{ key: "project", kind: "project", label: "Project", root, children }];
}

function runtime(node: ProjectNode): ProjectRuntimeTopic {
  return { scope: "project", workspaceRoot: root, node: { ...node, open: true, running: true, status: "thinking" } };
}

test("adoption replaces a pinned source in first and incomplete pages", () => {
  const source = { ...legacySession("b"), pinned: true };
  const adopted = { ...canonicalSession("b"), identityAliases: [projectSessionIdentity(source)], pinned: true };
  for (const merge of [
    (rows: ProjectNode[], page: ProjectNode[]) => mergeProjectTopicPage(rows, page, false),
    mergeIncompleteProjectTopicPage,
  ]) {
    const rows = merge([source], [adopted]);
    assert.equal(rows.length, 1, "source and canonical aliases are one actionable row");
    assert.equal(rows[0]?.session?.sessionId, "b");
    assert.deepEqual(merge(rows, [source]), rows, "a delayed source page cannot undo adoption");
  }
});

test("mounted row keys isolate hosts even when protocol display keys are equal", () => {
  const a = { ...canonicalSession("same-id"), key: "same-display-key", session: { hostId: "host-a", sessionId: "same-id" } };
  const b = { ...a, session: { hostId: "host-b", sessionId: "same-id" } };
  assert.notEqual(projectSessionRowKey(a), projectSessionRowKey(b));
});

test("opening one legacy source does not select a sibling with the same topic", () => {
  const a = legacySession("a");
  const b = legacySession("b");
  assert.equal(topicIsActive(a, "project", root, topicId, a.sessionPath), true);
  assert.equal(topicIsActive(b, "project", root, topicId, a.sessionPath), false);
});

test("full App chrome passes a canonical tab without sessionPath as an exact sidebar target", () => {
  type Input = Parameters<typeof buildSidebarRegionProps>[0];
  const props = buildSidebarRegionProps({
    className: "", t: (key => key) as Input["t"], paletteShortcut: "",
    shell: { sidebarCollapsed: false } as Input["shell"],
    geometry: { sidebarResizeMinWidth: 264, sidebarRenderWidth: 264 } as Input["geometry"],
    topics: {} as Input["topics"], commands: {} as Input["commands"],
    projectTree: {
      activeTab: { id: "tab-b", scope: "project", workspaceRoot: root, topicId,
        session: { hostId: "local", sessionId: "b" }, sessionId: "b" } as Input["projectTree"]["activeTab"],
      activeRemote: undefined,
      imTopicSources: {}, refreshSignal: 0, searchExpanded: false, searchFocusSignal: 0,
      showShortcutBadges: false, shortcutPlatform: undefined, onVisibleTopicsChange: () => {},
    },
  });
  const active = props.projectTree;
  assert.equal(active.activeSessionPath, "session-id:b");
  assert.equal(topicIsActive(canonicalSession("b"), active.activeScope, active.activeWorkspaceRoot, active.activeTopicId, active.activeSessionPath), true);
  assert.equal(topicIsActive(canonicalSession("a"), active.activeScope, active.activeWorkspaceRoot, active.activeTopicId, active.activeSessionPath), false);
});

test("legacy sources sharing a topic have distinct read records", () => {
  const a = legacySession("a");
  const b = legacySession("b");
  const aKey = projectTreeReadActivityKey(a);
  const bKey = projectTreeReadActivityKey(b);
  assert.ok(aKey);
  assert.ok(bKey);
  assert.notEqual(aKey, bKey);
});

test("viewing legacy A cannot hide a new unread result in legacy B", () => {
  const a = legacySession("a", 100);
  const b = legacySession("b", 20);
  const bKey = projectTreeReadActivityKey(b)!;
  assert.equal(projectTreeTopicHasUnreadActivity(
    b, { [bKey]: 10 }, "project", root, topicId, a.sessionPath,
  ), true);
});

test("a session tombstone fences stale runtime after the archived row leaves the catalog", () => {
  const a = canonicalSession("a");
  const b = canonicalSession("b");
  const projection = createProjectTreeRuntimeProjection();
  const running = projection.apply(catalog(a, b), [runtime(b)]);
  const tombstones = new Set([projectSessionIdentity(b)]);
  const archived = projectTreeWithoutTopics(running, tombstones);
  const delayed = projection.apply(archived, [runtime(b)], tombstones);
  assert.deepEqual(delayed[0]?.children?.map(row => row.session?.sessionId), ["a"]);
  assert.deepEqual(projection.apply(delayed, [], tombstones)[0]?.children?.map(row => row.session?.sessionId), ["a"]);
});

test("a session tombstone fences an incoming stale directory and runtime together", () => {
  const a = canonicalSession("a");
  const b = canonicalSession("b");
  const tombstones = new Set([projectSessionIdentity(b)]);
  const projected = createProjectTreeRuntimeProjection().apply(catalog(a, b), [runtime(b)], tombstones);
  assert.deepEqual(projected[0]?.children?.map(row => row.session?.sessionId), ["a"]);
});

test("adoption merges a legacy catalog source with its canonical runtime without losing its row metadata", () => {
  const source = legacySession("b");
  const adopted = { ...canonicalSession("b"), identityAliases: [projectSessionIdentity(source)] };
  const projected = createProjectTreeRuntimeProjection().apply(catalog(source), [runtime(adopted)]);
  assert.equal(projected[0]?.children?.length, 1);
  const row = projected[0]?.children?.[0];
  assert.equal(row?.session?.sessionId, "b");
  assert.equal(row?.label, source.label);
  assert.equal(row?.sessionPath, source.sessionPath);
  assert.equal(row?.status, "thinking");
});

test("a delayed ready catalog cannot lower an established read baseline and make old results unread", () => {
  const b = canonicalSession("b", 20);
  const key = projectTreeReadActivityKey(b)!;
  const read = { [key]: 20 };
  const delayed = projectTreeSeedReadActivity(catalog({ ...b, resultSequence: 10 }), read);
  assert.equal(delayed[key], 20);
  const current = projectTreeSeedReadActivity(catalog(b), delayed);
  assert.equal(current[key], 20);
  assert.equal(projectTreeTopicHasUnreadActivity(b, current), false);
});

test("runtime source reconciliation never aliases equal paths belonging to different hosts", () => {
  const local = canonicalSession("same");
  const remote = { ...canonicalSession("same"), session: { hostId: "remote", sessionId: "same" } };
  const projected = createProjectTreeRuntimeProjection().apply(catalog(local), [runtime(remote)]);
  assert.equal(projected[0]?.children?.length, 2);
  const localRow = projected[0]?.children?.find(row => row.session?.hostId === "local");
  assert.equal(localRow?.running, undefined);
});
