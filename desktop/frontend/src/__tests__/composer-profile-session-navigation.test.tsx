import assert from "node:assert/strict";
import React, { act, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useTabProjectionLifecycle } from "../app-runtime/useTabProjectionLifecycle";
import { useComposerProfileProjection } from "../app-runtime/useComposerProfileProjection";
import { projectControllerProfiles } from "../app-runtime/controllerProfileOwner";
import { useSessionOperations } from "../app-runtime/useSessionOperations";
import { sessionIdentityKey } from "../app-runtime/sessionTarget";
import { useControllerProfileCommands } from "../lib/useControllerProfileCommands";
import type { ComposerProfile, UserPlanModeIntents } from "../lib/composerProfile";
import type { Meta, TabMeta, ToolApprovalMode } from "../lib/types";
import type { RemoteSessionApi } from "../lib/useRemoteSession";

const dom = new JSDOM("<div id='root'></div>");
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
const root = createRoot(document.getElementById("root")!);

const pushes: string[] = [];
const ports = {
  model: async () => true,
  profile: async (_tab: string, _collaboration: string, approval: string) => { pushes.push(approval); return true; },
};
const remoteSession = { composerProfile: undefined } as unknown as RemoteSessionApi;
let patchActive!: ReturnType<typeof useComposerProfileProjection>["patchActiveComposerProfile"];

function tabFor(sessionId: string, preset: ToolApprovalMode): TabMeta {
  return { id: "surface", active: true, scope: "project", workspaceRoot: "/w", topicId: "topic-" + sessionId,
    sessionId, session: { hostId: "local", sessionId }, toolApprovalMode: preset } as unknown as TabMeta;
}
function metaFor(sessionId: string, preset: ToolApprovalMode): Meta {
  return { ready: true, sessionId, session: { hostId: "local", sessionId }, toolApprovalMode: preset } as unknown as Meta;
}

// Mirrors the App composition: backend tab and meta projections hydrate the
// per-tab profile table, and the controller profile owner pushes it back.
function Surface({ tab, meta }: { tab: TabMeta; meta: Meta }) {
  const [profiles, setProfiles] = useState<Record<string, ComposerProfile>>({});
  const [, setOrder] = useState<string[]>([]);
  const planIntentsRef = useRef<UserPlanModeIntents>({});
  const tabs = [tab];
  useTabProjectionLifecycle({ tabs, activeTabId: tab.id, activeMeta: tab, meta, planIntentsRef, setOrder, setProfiles });
  const projection = useComposerProfileProjection({ activeTabId: tab.id, activeTab: tab, meta, profilesByTab: profiles,
    setProfilesByTab: setProfiles, tabMetas: tabs, remote: false, remoteSession, planIntentsRef });
  patchActive = projection.patchActiveComposerProfile;
  const target = { tabId: tab.id, sessionKey: sessionIdentityKey({ tabId: tab.id, session: tab.session, sessionId: tab.sessionId,
    scope: tab.scope, workspaceRoot: tab.workspaceRoot, topicId: tab.topicId }) };
  const controllerProfiles = projectControllerProfiles(tabs, profiles, { target, profile: projection.composerProfile, remote: false });
  const operations = useSessionOperations({ visible: target, resources: controllerProfiles.map(value => value.target) });
  useControllerProfileCommands({ target, profiles: controllerProfiles, ready: true, remote: false, operations, ports,
    remoteModel: async () => {}, report: error => { throw error; } });
  return null;
}

const paint = (tab: TabMeta, meta: Meta) => act(async () => root.render(<Surface tab={tab} meta={meta} />));

try {
  for (const tabFirst of [true, false]) {
    const order = tabFirst ? "tab list before meta" : "meta before tab list";
    const navigate = async (sessionId: string, preset: ToolApprovalMode, from: { tab: TabMeta; meta: Meta }) => {
      if (tabFirst) await paint(tabFor(sessionId, preset), from.meta);
      else await paint(from.tab, metaFor(sessionId, preset));
      await paint(tabFor(sessionId, preset), metaFor(sessionId, preset));
    };

    await paint(tabFor("A", "danger-full-access"), metaFor("A", "danger-full-access"));
    pushes.length = 0;
    await navigate("B", "workspace-write", { tab: tabFor("A", "danger-full-access"), meta: metaFor("A", "danger-full-access") });
    assert.ok(!pushes.includes("danger-full-access"), `${order}: A's full access was pushed onto B: ${pushes.join(",")}`);

    await act(async () => patchActive({ toolApprovalMode: "read-only" }, ["toolApprovalMode"]));
    await paint(tabFor("B", "read-only"), metaFor("B", "read-only"));
    pushes.length = 0;
    await navigate("A", "danger-full-access", { tab: tabFor("B", "read-only"), meta: metaFor("B", "read-only") });
    assert.deepEqual(pushes.filter(approval => approval !== "danger-full-access"), [],
      `${order}: a push after returning to A overwrote its restored preset: ${pushes.join(",")}`);

    await act(async () => root.render(null));
  }
  console.log("composer profile session navigation: no push carries another session's preset");
} finally {
  await act(async () => root.unmount());
  dom.window.close();
}
