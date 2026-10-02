import assert from "node:assert/strict";
import { test } from "node:test";
import { draftLandingTargetForTab } from "../app-runtime/draftLandingTarget";

test("a project surface hands over to its own workspace draft", () => {
  assert.deepEqual(
    draftLandingTargetForTab({ scope: "project", workspaceRoot: "/repo/agent" }),
    { scope: "project", workspaceRoot: "/repo/agent" },
  );
});

test("a global surface keeps its actual directory for UI preferences", () => {
  assert.deepEqual(
    draftLandingTargetForTab({ scope: "global", workspaceRoot: "/home/user/.reasonix/global" }),
    { scope: "global", workspaceRoot: "/home/user/.reasonix/global" },
  );
});

test("remote, rootless and missing surfaces hand over to the global draft", () => {
  for (const tab of [
    undefined,
    null,
    { scope: "global", workspaceRoot: "" },
    { scope: "project", workspaceRoot: "" },
    { scope: "project", workspaceRoot: "/repo/agent", remote: { hostId: "host", workspace: "/w" } },
  ]) {
    assert.deepEqual(draftLandingTargetForTab(tab), { scope: "global", workspaceRoot: "" }, JSON.stringify(tab));
  }
});
