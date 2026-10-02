import { registerHooks } from "node:module";
registerHooks({ resolve(specifier, context, nextResolve) { return specifier.endsWith(".svg") ? nextResolve("./asset-stub-for-tests.ts", { ...context, parentURL: import.meta.url }) : nextResolve(specifier, context); } });
import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import type { SessionLifecycleRequest } from "../generated/desktopContract.generated";
import { installDesktopHostStub } from "./desktopHostStub";
const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { ArchivedSessionsList } = await import("../components/ArchivedSessionsList");
type Row = { id: string; ref: { hostId: string; sessionId: string }; title: string; workspaceId: string; workspaceTitle: string; archivedAt: number; health: string; canPreview: boolean; canRestore: boolean; canPurge: boolean };
const row = (id: string): Row => ({ id, ref: { hostId: "local", sessionId: id }, title: id, workspaceId: "global", workspaceTitle: "Global", archivedAt: 0, health: "ready", canPreview: true, canRestore: true, canPurge: true });

let generation = 7;
let rows = [row("victim")];
const requests: SessionLifecycleRequest[] = [];
let networkFailure = false;
let latePreview: (() => void) | undefined;
let holdPreview = false;
const host = installDesktopHostStub({
 ListTrashEntries: async () => ({items: rows, generation}),
 ReadSessionHistory: async () => holdPreview
  ? new Promise(resolve => { latePreview = () => resolve({messages:[{role:"user", content:"late private preview"}]}); })
  : { messages: [{role:"user", content:"preview"}] },
 ApplySessionLifecycle: async (request: SessionLifecycleRequest) => {
  requests.push(structuredClone(request));
  if (networkFailure) throw new Error("network outcome unknown");
  return {operationId: request.operationId, generation, committed: true, items: request.targets.map(target => ({target, committed:true, retryable:false}))};
 },
});
const root = createRoot(document.getElementById("root")!);
const button = (text: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button")).find(node => node.textContent?.trim() === text)!;
const startDelete = async () => {
 await act(async () => (document.querySelector('.archived-sessions__open') as HTMLButtonElement).click());
 await act(async () => (document.querySelector('.archived-sessions__session-menu') as HTMLButtonElement).click());
 await act(async () => (document.querySelector('[role="menuitem"]') as HTMLButtonElement).click());
};
await act(async () => root.render(<LocaleProvider><ArchivedSessionsList active={true} onOpenSession={async()=>{}} /></LocaleProvider>));
await startDelete();
assert.ok(document.querySelector('[role="dialog"]'));
// Another client restores and rearchives this same session while confirmation is open.
generation = 9;
await act(async () => host.emit("project-tree:changed"));
await act(async () => button("Permanently delete").click());
assert.equal(requests[0].expectedGeneration, 7, "confirmation retains original generation");

// A background refresh must not remove an unknown-result request or its retry.
networkFailure = true;
await startDelete();
await act(async () => button("Permanently delete").click());
const unknown = structuredClone(requests.at(-1));
generation = 12;
await act(async () => host.emit("project-tree:changed"));
assert.ok(button("Retry failed items"), "refresh preserves retry control");
await act(async () => button("Retry failed items").click());
assert.deepEqual(requests.at(-1), unknown, "retry preserves the complete original request");

// Invalidate an outstanding preview when its row becomes unavailable.
holdPreview = true;
act(() => (document.querySelector('.archived-sessions__open') as HTMLButtonElement).click());
rows = [{...row("victim"), canPreview:false, canRestore:false}];
await act(async () => host.emit("project-tree:changed"));
assert.equal(document.querySelector('.archived-sessions__preview-heading'), null, "unavailable preview content is cleared");
latePreview?.();
await Promise.resolve();
assert.ok(!document.body.textContent?.includes("late private preview"));

// Cancelling and leaving the page never submits the captured request.
const beforeCancel = requests.length;
await act(async () => (document.querySelector('.archived-sessions__delete') as HTMLButtonElement).click());
await act(async () => button("Cancel").click());
assert.equal(requests.length, beforeCancel);
// Replacing an open confirmation cancels its old request; only the replacement submits.
await act(async () => (document.querySelector('.archived-sessions__delete') as HTMLButtonElement).click());
generation = 15;
await act(async () => host.emit("project-tree:changed"));
await act(async () => (document.querySelector('.archived-sessions__delete') as HTMLButtonElement).click());
assert.equal(requests.length, beforeCancel);
await act(async () => button("Permanently delete").click());
assert.equal(requests.length, beforeCancel + 1);
assert.equal(requests.at(-1)?.expectedGeneration, 15);
await act(async () => (document.querySelector('.archived-sessions__delete') as HTMLButtonElement).click());
await act(async () => root.render(<LocaleProvider><ArchivedSessionsList active={false} onOpenSession={async()=>{}} /></LocaleProvider>));
assert.equal(document.querySelector('[role="dialog"]'), null);
assert.equal(requests.length, beforeCancel + 1);
await act(async () => root.unmount());
dom.window.close();
console.log("PASS frozen confirmation, unchanged unknown-result retry, refresh invalidation and cancelled confirmation");
