import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { ArchivedSessionsList } = await import("../components/ArchivedSessionsList");
const row = (id: string) => ({ id, ref: { hostId: "local", sessionId: id }, title: id, workspaceId: "global", workspaceTitle: "Global", archivedAt: 0, health: "ready", canPreview: true, canRestore: true, canPurge: true });
let rows = [row("alpha"), row("beta")];
let generation = 1;
const requests: Array<{ action: string; targets: Array<{ ref: { sessionId: string } }> }> = [];
const host = installDesktopHostStub({
  ListTrashEntries: async () => ({ items: rows, generation }),
  ApplySessionLifecycle: async (request: { action: string; targets: Array<{ ref: { sessionId: string } }> }) => {
    requests.push(request);
    rows = rows.filter(item => !request.targets.some(target => target.ref.sessionId === item.id));
    generation++;
    return { committed: true, generation, items: request.targets.map(target => ({ target, committed: true, retryable: false })) };
  },
});
const root = createRoot(document.getElementById("root")!);
const button = (name: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(node => node.textContent?.trim() === name)!;
const openClear = async () => {
  await act(async () => (document.querySelector(".archived-sessions__management") as HTMLButtonElement).click());
  await act(async () => (document.querySelector('[role="menuitem"]:last-child') as HTMLButtonElement).click());
};
await act(async () => root.render(<LocaleProvider><ArchivedSessionsList active onOpenSession={async () => {}} /></LocaleProvider>));

// Filtering changes what is visible, never the frozen bulk target set.
const search = document.querySelector<HTMLInputElement>(".archived-sessions__search input")!;
await act(async () => {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search, "alpha");
  search.dispatchEvent(new Event("input", { bubbles: true }));
});
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 1);
await openClear();
assert.ok(document.querySelector('[role="dialog"]')?.textContent?.includes("all 2 archived conversations"));
await act(async () => button("Cancel").click());
assert.equal(requests.length, 0);
await openClear();
await act(async () => button("Delete all 2 conversations").click());
assert.deepEqual(requests[0].targets.map(target => target.ref.sessionId), ["alpha", "beta"]);

// The clear-all route stays a bulk confirmation even with only one archive.
rows = [row("solo")];
await act(async () => host.emit("project-tree:changed"));
await openClear();
assert.ok(document.querySelector('[role="dialog"]')?.textContent?.includes("all 1 archived conversations"));
assert.equal(button("Delete all 1 conversations")?.textContent, "Delete all 1 conversations");
await act(async () => button("Cancel").click());

await act(async () => root.unmount());
host.uninstall();
dom.window.close();
console.log("PASS filtered bulk scope, cancellation, captured targets and single-entry clear-all wording");
