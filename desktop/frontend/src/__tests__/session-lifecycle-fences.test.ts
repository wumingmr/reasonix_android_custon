import assert from "node:assert/strict";
import { test } from "node:test";
import { createSessionLifecycleFences, sessionLifecycleFences } from "../lib/sessionLifecycleFences";
import { createProjectTreeRuntimeProjection } from "../lib/projectTreeRuntime";
import { projectSessionIdentity } from "../lib/projectSessionIdentity";
import type { ProjectNode, ProjectRuntimeTopic } from "../lib/types";

function session(id: string, lifecycleGeneration = 10): ProjectNode {
  return {
    key: id, kind: "topic", topicId: "shared-topic", label: id, root: "/repo",
    session: { hostId: "local", sessionId: id }, lifecycleGeneration, children: [],
  };
}
function catalog(...rows: ProjectNode[]): ProjectNode[] {
  return [{ key: "project", kind: "project", root: "/repo", label: "Project", children: rows }];
}
function runtime(node: ProjectNode): ProjectRuntimeTopic[] {
  return [{ scope: "project", workspaceRoot: "/repo", node: { ...node, running: true, runtimeOnly: true } }];
}

test("the application fence survives sidebar projection disposal and remount", () => {
  const archived = session("remount-archived");
  const sibling = session("remount-sibling");
  sessionLifecycleFences.archive(archived, { lifecycleGeneration: 10, operationId: "archive-remount" });
  try {
    const firstMount = createProjectTreeRuntimeProjection().apply(catalog(sibling, archived), runtime(archived), sessionLifecycleFences.keys());
    assert.deepEqual(firstMount[0]?.children?.map(node => node.session?.sessionId), ["remount-sibling"]);
    const secondMount = createProjectTreeRuntimeProjection().apply(catalog(sibling, archived), runtime(archived), sessionLifecycleFences.keys());
    assert.deepEqual(secondMount[0]?.children?.map(node => node.session?.sessionId), ["remount-sibling"]);
    assert.equal(sessionLifecycleFences.keys().has(projectSessionIdentity(archived)), true);
  } finally {
    sessionLifecycleFences.observeDirectory([session("remount-archived", 11)]);
  }
});

test("old directory rows and runtime-only rows cannot release a committed archive fence", () => {
  const fences = createSessionLifecycleFences();
  const archived = session("b", 20);
  const alias = "source\x00local\x00legacy-b";
  fences.archive(archived, { lifecycleGeneration: 20, operationId: "archive-b", identityAliases: [alias] });
  fences.observeDirectory(catalog(session("b", 19)));
  fences.observeDirectory(catalog(session("b", 20)));
  fences.observeDirectory(catalog({ ...session("b", 999), runtimeOnly: true }));
  assert.equal(fences.keys().has(projectSessionIdentity(archived)), true);
  assert.equal(fences.keys().has(alias), true);
  const projected = createProjectTreeRuntimeProjection().apply(catalog(), runtime(session("b", 999)), fences.keys());
  assert.deepEqual(projected[0]?.children, []);
});

test("a newer restored directory row releases only its exact session and source aliases", () => {
  const fences = createSessionLifecycleFences();
  const a = session("a");
  const b = session("b");
  const aliasA = "source\x00local\x00legacy-a";
  const aliasB = "source\x00local\x00legacy-b";
  fences.archive(a, { lifecycleGeneration: 10, operationId: "archive-a", identityAliases: [aliasA] });
  fences.archive(b, { lifecycleGeneration: 10, operationId: "archive-b", identityAliases: [aliasB] });
  const restored = session("a", 11);
  fences.observeDirectory(catalog(restored, b));
  assert.equal(fences.keys().has(projectSessionIdentity(a)), false);
  assert.equal(fences.keys().has(aliasA), false);
  assert.equal(fences.keys().has(projectSessionIdentity(b)), true);
  assert.equal(fences.keys().has(aliasB), true);
  const projected = createProjectTreeRuntimeProjection().apply(catalog(restored, b), runtime(b), fences.keys());
  assert.deepEqual(projected[0]?.children?.map(node => node.session?.sessionId), ["a"]);
});

test("a late archive receipt cannot weaken a newer fence for the same session", () => {
  const fences = createSessionLifecycleFences();
  const b = session("b");
  fences.archive(b, { lifecycleGeneration: 30, operationId: "new-archive" });
  fences.archive(b, { lifecycleGeneration: 10, operationId: "old-archive" });
  fences.observeDirectory([session("b", 20)]);
  assert.equal(fences.keys().has(projectSessionIdentity(b)), true);
  fences.observeDirectory([session("b", 31)]);
  assert.equal(fences.keys().has(projectSessionIdentity(b)), false);
});
