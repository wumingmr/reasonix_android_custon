import assert from "node:assert/strict";
import { rebindMockSessionTab } from "../lib/bridgeMockSessionNavigation";
import type { TabMeta } from "../lib/types";

const source = {
  id: "tab-stable",
  scope: "project",
  workspaceRoot: "/old",
  workspaceName: "old",
  topicId: "legacy-old",
  topicTitle: "Old",
  label: "model",
  ready: true,
  running: false,
  mode: "normal",
  active: true,
  cwd: "/old",
} as TabMeta;

const rebound = rebindMockSessionTab(
  { hostId: "local", sessionId: "canonical-target" }, source,
  [{ key: "workspace-new", kind: "project", root: "/new", label: "new", children: [
    { key: "canonical-target", kind: "topic", topicId: "canonical-target", label: "Target" },
 ] }], "/global", () => false,
);

assert.equal(rebound.id, source.id, "SessionRef navigation preserves the surface identity");
assert.deepEqual(rebound.session, { hostId: "local", sessionId: "canonical-target" });
assert.equal(rebound.sessionId, "canonical-target");
assert.equal(rebound.workspaceId, "project--new");
assert.equal(rebound.topicId, "canonical-target", "legacy fixture metadata follows the rebound session");
assert.throws(() => rebindMockSessionTab(
  { hostId: "local", sessionId: "missing" }, undefined, [], "/global", () => false,
), /not ready/, "navigation cannot invent a replacement surface");

console.log("PASS mock SessionRef navigation rebinds the existing tab identity");
