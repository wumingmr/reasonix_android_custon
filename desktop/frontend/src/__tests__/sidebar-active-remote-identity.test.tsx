// Run: node --import ./scripts/svg-stub-register.mjs --import tsx src/__tests__/sidebar-active-remote-identity.test.tsx
// The project tree keys ancestor expansion, read marking and its row
// projection on the active remote reference, so a new object per render would
// re-run every tree walk on each transcript delta. The reference is owned by
// useActiveRemoteRef and the chrome assembly must pass it through untouched.
import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useActiveRemoteRef } from "../app-runtime/useActiveRemoteRef";
import { buildSidebarRegionProps } from "../app-shell/chromeRegionBuilders";
import type { RemoteTabRefView, TabMeta } from "../lib/types";

const dom = new JSDOM("<div id='root'></div>");
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });

const tab = (overrides: Partial<TabMeta>): TabMeta => ({
  id: "tab-remote", scope: "project", workspaceRoot: "~/app", workspaceName: "app", topicId: "", topicTitle: "app",
  label: "box", ready: true, running: false, mode: "normal", active: true, cwd: "~/app", ...overrides,
} as TabMeta);

const seen: Array<RemoteTabRefView | undefined> = [];
function Probe({ meta }: { meta: TabMeta | undefined }) {
  seen.push(useActiveRemoteRef(meta));
  return null;
}
const root = createRoot(document.getElementById("root")!);
const show = (meta: TabMeta | undefined) => act(async () => { root.render(React.createElement(Probe, { meta })); });

try {
  const remoteTab = tab({ remote: { hostId: "box", workspace: "~/app" }, sessionId: "session-a" });
  await show(remoteTab);
  const first = seen[seen.length - 1];
  assert.deepEqual(first, { hostId: "box", workspace: "~/app", sessionId: "session-a" }, "the reference carries the host, workspace and bound session");

  // A transcript delta re-renders the shell with the same tab meta.
  await show(remoteTab);
  assert.equal(seen[seen.length - 1], first, "re-rendering with the same tab meta keeps the reference identity");

  // A tab-meta refresh (running flag, turn counters) replaces the meta and its
  // nested remote object while the session binding is unchanged.
  await show(tab({ remote: { hostId: "box", workspace: "~/app" }, sessionId: "session-a", running: true, turnId: "t1" }));
  assert.equal(seen[seen.length - 1], first, "an unrelated tab-meta refresh does not churn the reference");

  await show(tab({ remote: { hostId: "box", workspace: "~/app" }, sessionId: "session-b" }));
  const rotated = seen[seen.length - 1];
  assert.notEqual(rotated, first, "rotating to another session publishes a new reference");
  assert.equal(rotated?.sessionId, "session-b");

  await show(tab({ remote: { hostId: "other-box", workspace: "~/app" }, sessionId: "session-b" }));
  assert.notEqual(seen[seen.length - 1], rotated, "moving to another host publishes a new reference");

  await show(tab({}));
  assert.equal(seen[seen.length - 1], undefined, "a local tab has no active remote reference");
  await show(undefined);
  assert.equal(seen[seen.length - 1], undefined, "a draft surface (no active tab) has no active remote reference");

  // The chrome assembly is a pure pass-through: rebuilding the object there
  // would defeat the memo regardless of how stable its input is.
  type Input = Parameters<typeof buildSidebarRegionProps>[0];
  const props = buildSidebarRegionProps({
    className: "", t: ((key: string) => key) as Input["t"], paletteShortcut: "",
    shell: { sidebarCollapsed: false } as Input["shell"],
    geometry: { sidebarResizeMinWidth: 264, sidebarRenderWidth: 264 } as Input["geometry"],
    topics: {} as Input["topics"], commands: {} as Input["commands"],
    projectTree: {
      activeTab: remoteTab, activeRemote: first,
      imTopicSources: {}, refreshSignal: 0, searchExpanded: false, searchFocusSignal: 0,
      showShortcutBadges: false, shortcutPlatform: undefined, onVisibleTopicsChange: () => {},
    },
  });
  assert.equal(props.projectTree.activeRemote, first, "the sidebar assembly forwards the owned reference by identity");
  console.log("sidebar active remote identity: stable across deltas and meta refreshes, rotates on host/session change, forwarded by identity");
} finally {
  await act(async () => root.unmount());
}
