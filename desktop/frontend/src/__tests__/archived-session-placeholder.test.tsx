import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { ArchivedSessionsList } = await import("../components/ArchivedSessionsList");

const requests: Array<{ targets: Array<{ workspaceId?: string; recoveryEntryId?: string; ref?: unknown }> }> = [];
const opened: string[] = [];
let restored = false;
let cleanupRetries = 0;
const cleanupStatus = {
  version: 1, batchId: "batch", state: "pending", removed: 1, pending: 2,
  busy: 1, unknown: 1, protected: 0, hasContent: 0, items: [],
};
const placeholder = {
  id: "topic:legacy-placeholder",
  recoveryEntryId: "legacy-cleanup:topic:legacy-placeholder",
  title: "New session",
  workspaceId: "workspace-a",
  workspaceTitle: "Project A",
  archivedAt: 42,
  health: "ready",
  canRestore: true,
  canPreview: false,
  canPurge: false,
  cleanupBatchId: "batch",
  cleanupKind: "topic_placeholder",
};
const host = installDesktopHostStub({
  ListTrashEntries: async () => ({ items: restored ? [] : [placeholder], generation: 7 }),
  GetLegacyEmptySessionCleanupStatus: async () => cleanupStatus,
  RetryLegacyEmptySessionCleanup: async () => { cleanupRetries++; return cleanupStatus; },
  ApplySessionLifecycle: async (request: { targets: Array<{ workspaceId?: string; recoveryEntryId?: string; ref?: unknown }> }) => {
    requests.push(request);
    restored = true;
    return {
      committed: true,
      generation: 8,
      items: request.targets.map(target => ({ target, workspaceId: target.workspaceId, committed: true, retryable: false })),
    };
  },
});

const root = createRoot(document.getElementById("root")!);
await act(async () => root.render(
  <LocaleProvider>
    <ArchivedSessionsList active={true} onOpenSession={async ref => { opened.push(ref.sessionId); }} />
  </LocaleProvider>,
));

const row = document.querySelector(".archived-sessions__row")!;
assert.ok(row.textContent?.includes("Legacy session placeholder"));
assert.equal((row.querySelector(".archived-sessions__open") as HTMLButtonElement).disabled, true, "placeholder has no transcript preview");
assert.ok(document.body.textContent?.includes("2 legacy sessions still need verification."));

assert.equal(Array.from(document.querySelectorAll("button")).some(button => button.textContent === "Recheck"), false,
  "retired cleanup must not expose an action that archives more sessions");
assert.equal(cleanupRetries, 0, "opening recovery must not resume cleanup");

await act(async () => (row.querySelector('[aria-label="Restore session"]') as HTMLButtonElement).click());
assert.equal(requests.length, 1);
assert.deepEqual(requests[0].targets, [{ workspaceId: "workspace-a", recoveryEntryId: "legacy-cleanup:topic:legacy-placeholder" }]);
assert.deepEqual(opened, [], "restoring a placeholder must not open or create a formal session");
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 0);

await act(async () => root.unmount());
host.uninstall();
dom.window.close();
console.log("PASS legacy cleanup placeholder restore keeps session identity absent");
