// Run: tsx src/__tests__/remote-composer-profile-sync.test.tsx
// useRemoteComposerProfileSync adopts the backend profile when a remote tab
// rotates to another session, but a tab switch away and back is not a
// rotation: the tab's in-flight mode/goal choice (pending flags) must survive
// A -> B -> A, and forgotten tabs must not read as rotations when reopened.
import assert from "node:assert/strict";
import React, { act, useState } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useRemoteComposerProfileSync } from "../lib/useRemoteComposerIntegration";
import type { ComposerProfile, ComposerProfilesByTab } from "../lib/composerProfile";
import type { RemoteSessionApi } from "../lib/useRemoteSession";

const dom = new JSDOM("<div id='root'></div>");
Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });

type RemoteProfile = NonNullable<RemoteSessionApi["composerProfile"]>;
const backend: RemoteProfile = { collaborationMode: "normal", toolApprovalMode: "ask", goal: "", qualityFloor: "standard" };
const chosen = (tabId: string): ComposerProfile => ({ collaborationMode: "plan", goalDraftMode: false, toolApprovalMode: "ask", goal: "", qualityFloor: "standard", pending: { collaborationMode: true } });
let profiles: ComposerProfilesByTab = {};
let setProfilesRef: React.Dispatch<React.SetStateAction<ComposerProfilesByTab>> | undefined;
function Probe({ activeTabId, sessionRoute }: { activeTabId: string; sessionRoute: string }) {
  const [state, setState] = useState<ComposerProfilesByTab>({ A: chosen("A"), B: chosen("B") });
  profiles = state;
  setProfilesRef = setState;
  const profile = state[activeTabId];
  useRemoteComposerProfileSync({ activeTabId, sessionRoute, remote: true, remoteProfile: backend,
    collaborationMode: profile?.collaborationMode ?? "normal", toolApprovalMode: profile?.toolApprovalMode ?? "ask",
    goal: profile?.goal ?? "", qualityFloor: profile?.qualityFloor ?? "standard", pending: profile?.pending ?? {}, setProfiles: setState });
  return null;
}
const root = createRoot(document.getElementById("root")!);
const show = (activeTabId: string, sessionRoute: string) => act(async () => { root.render(React.createElement(Probe, { activeTabId, sessionRoute })); });
const edit = (update: (current: ComposerProfilesByTab) => ComposerProfilesByTab) => act(async () => { setProfilesRef?.(update); });

try {
  await show("A", "session-id:a1");
  assert.equal(profiles.A.pending.collaborationMode, true, "first sync of a tab reconciles over its existing choice");
  assert.equal(profiles.A.collaborationMode, "plan");
  await show("B", "session-id:b1");
  assert.equal(profiles.B.pending.collaborationMode, true, "B's first sync keeps B's choice");
  await show("A", "session-id:a1");
  assert.equal(profiles.A.pending.collaborationMode, true, "A -> B -> A preserves A's pending choice");
  assert.equal(profiles.A.collaborationMode, "plan", "A -> B -> A keeps the chosen mode visible");
  assert.equal(profiles.B.pending.collaborationMode, true, "switching back to A leaves B untouched");
  await show("A", "session-id:a2");
  assert.deepEqual(profiles.A.pending, {}, "A's own session rotation adopts the backend profile");
  assert.equal(profiles.A.collaborationMode, "normal", "the rotated session shows the backend mode");
  assert.equal(profiles.B.pending.collaborationMode, true, "A's rotation does not touch B");
  // Closing B drops it from the profile table; the next sync (any status or
  // route change on the active tab) forgets B's route, so a later tab with
  // the same id on another session reads as a first sync, not a rotation.
  await edit(({ B: _closed, ...rest }) => rest);
  await show("A", "session-id:a3");
  await edit((current) => ({ ...current, B: chosen("B") }));
  await show("B", "session-id:b2");
  assert.equal(profiles.B.pending.collaborationMode, true, "a tab id forgotten after its removal is not treated as a session rotation");
  await show("B", "session-id:b3");
  assert.deepEqual(profiles.B.pending, {}, "the re-registered tab still adopts the backend on its next real rotation");
  console.log("remote composer profile sync: per-tab route memory, A->B->A preservation, rotation adoption and pruning passed");
} finally {
  await act(async () => root.unmount());
}
