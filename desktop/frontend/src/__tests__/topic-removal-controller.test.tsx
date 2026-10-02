import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useProjectTreeArchiveController } from "../lib/projectTreeArchive";
import { LocaleProvider } from "../lib/i18n";
import { installDesktopHostStub } from "./desktopHostStub";
import type { TopicRemovalRequest, TopicRemovalResult } from "../generated/desktopContract.generated";

const dom = new JSDOM("<div id='root'></div>", { url: "http://localhost" });
Object.assign(globalThis, { window: dom.window, document: dom.window.document, localStorage: dom.window.localStorage, IS_REACT_ACT_ENVIRONMENT: true });
let disposition = "discard_placeholder";
let confirm = false;
let confirmations = 0;
let inspections = 0;
let removed = 0;
let refreshed = 0;
const requests: TopicRemovalRequest[] = [];
const errors: string[] = [];
const invalidated: string[] = [];
let response: TopicRemovalResult = { committed: true, disposition, retryable: false };
const stub = installDesktopHostStub({
  InspectTopicRemoval: async (target: { topicId: string }) => { inspections++; return { target: { workspaceId: "global", topicId: target.topicId }, disposition, allowed: true, token: target.topicId }; },
  ConfirmAction: async () => { confirmations++; return confirm; },
  RemoveTopic: async (request: TopicRemovalRequest) => { requests.push(request); return response; },
  TrashTopic: async () => { throw new Error("must use the typed removal receipt"); },
});
let owner!: ReturnType<typeof useProjectTreeArchiveController>;
const props: Parameters<typeof useProjectTreeArchiveController>[0] = {
  treeRef: { current: [{ key: "fixture", kind: "project", label: "Fixture", root: "/fixture", children:
    ["empty", "named"].map(topicId => ({ key: topicId, topicId, kind: "topic", label: topicId })) }] },
  invalidateProjectTopicLists: key => { invalidated.push(key); },
  refreshRef: { current: async () => { refreshed++; } },
  optimisticallyRemoveTopic: () => { removed++; }, optimisticallyRemoveSession: () => {}, closeMenu: () => {},
  showToast: (message: string) => { errors.push(message); },
};
function Probe() { owner = useProjectTreeArchiveController(props); return null; }
const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => root.render(<LocaleProvider><Probe /></LocaleProvider>));
  await act(async () => { await owner.trashTopic("empty"); });
  assert.equal(confirmations, 0, "default empty placeholders do not require confirmation");
  assert.equal(removed, 1);
  assert.deepEqual(invalidated, ["fixture"], "committed removal retires the owning list");
  disposition = "archive_placeholder";
  await act(async () => { await owner.trashTopic("named"); });
  assert.equal(confirmations, 1);
  assert.equal(requests.length, 1, "cancel does not mutate");
  assert.equal(removed, 1, "cancel never installs a success tombstone");
  assert.equal(invalidated.length, 1, "cancellation leaves list ownership intact");
  confirm = true;
  response = { committed: false, disposition, retryable: true, errorCode: "operation_failed" };
  await act(async () => { await owner.trashTopic("named"); });
  assert.equal(removed, 1, "partial/failed mutations stay authoritative until recovery reload");
  assert.equal(errors.length, 1);
  assert.equal(invalidated.length, 1, "failed removal does not commit list invalidation");
  const inspectedBeforeRetry = inspections;
  response = { committed: true, disposition, retryable: false, recoveryEntryId: "topic-removal:receipt" };
  await act(async () => { await owner.trashTopic("named"); });
  assert.equal(inspections, inspectedBeforeRetry, "retry works even after a partial removal hid the metadata");
  assert.deepEqual(requests[1], requests[2], "retry retains operation identity and confirmation token");
  assert.equal(removed, 2);
  assert.deepEqual(invalidated, ["fixture", "fixture"], "successful retry invalidates through the shared list owner");
  assert.ok(refreshed >= 3);
  await act(async () => root.unmount());
  console.log("PASS topic removal: confirmation, cancellation, typed commit and idempotent retry");
} finally { stub.uninstall(); dom.window.close(); }
