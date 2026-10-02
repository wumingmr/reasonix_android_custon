import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import { installDesktopHostStub } from "./desktopHostStub";
const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { HistoricalRecoveryList } = await import("../components/HistoricalRecoveryList");
let failRefresh = false;
let failRestore = true;
const restores: string[] = [];
const opened: string[] = [];
const host = installDesktopHostStub({
  ListRecoveryEntries: async (_query: string, cursor: string, limit: number) => {
    assert.equal(limit, 50);
    if (failRefresh) throw new Error("list unavailable");
    return { items: [{ id: cursor ? "second" : "first", title: cursor ? "Second" : "First", canRestore: true, canPreview: true }], nextCursor: cursor ? "" : "next", generation: 4 };
  },
  PreviewRecoveryEntry: async () => ({ messages: [{ messageId: "message", content: "Preserved history" }] }),
  ApplySessionLifecycle: async (request: { operationId: string; targets: { recoveryEntryId: string }[] }) => {
    const id = request.targets[0].recoveryEntryId;
    restores.push(request.operationId);
    if (failRestore) throw new Error("source unavailable");
    failRefresh = true;
    return { items: [{ committed: true, ref: { hostId: "local", sessionId: id }, workspaceId: "global" }], generation: 8 };
  },
});
const root = createRoot(document.getElementById("root")!);
await act(async () => root.render(<LocaleProvider><HistoricalRecoveryList active onOpenSession={async ref => { opened.push(ref.sessionId); }} /></LocaleProvider>));
const button = (text: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(node => node.textContent?.trim() === text)!;
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 1);
await act(async () => button("Load more").click());
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 2);
await act(async () => button("Restore").click());
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 2, "failed restore keeps its source entry");
failRestore = false;
await act(async () => button("Restore").click());
assert.equal(restores[0], restores[1], "retry reuses the same operation identity");
assert.deepEqual(opened, ["first"]);
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 1, "durable success survives a failed list refresh");
assert.ok(document.querySelector('[role="alert"]')?.textContent?.toLowerCase().includes("restored"));
assert.equal(document.querySelector(".history-clear"), null, "historical entries cannot be bulk purged");
await act(async () => root.unmount());
host.uninstall(); dom.window.close();
console.log("PASS historical pagination, failed restore retention, idempotent retry and committed refresh failure");
