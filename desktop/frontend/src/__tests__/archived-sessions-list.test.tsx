import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import { installDesktopHostStub } from "./desktopHostStub";
const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { ToastProvider } = await import("../lib/toast");
const { ArchivedSessionsList } = await import("../components/ArchivedSessionsList");
const row = (id: string) => ({ id, ref: { hostId: "local", sessionId: id }, title: id, workspaceId: "hidden", workspaceTitle: "Hidden project", archivedAt: 0, health: "ready", canRestore: true, canPreview: true, canPurge: true });
const calls: string[] = [];
let restored = false, late = false;
let resolveLate: ((value: unknown) => void) | undefined;
const host = installDesktopHostStub({
  ListTrashEntries: async (_query: string, cursor: string) => {
    calls.push(cursor);
    if (late) return new Promise(resolve => { resolveLate = resolve; });
    return cursor ? { items: restored ? [] : [row("archived")], generation: 1 }
      : { items: [], generation: 1, nextCursor: "page-2" };
  },
  ReadSessionHistory: async () => ({ messages: [{ messageId: "preview", role: "user", content: "Read-only history" }] }),
  ApplySessionLifecycle: async (request: {targets:{ref:{sessionId:string}}[]}) => {
    const target = request.targets[0]; assert.equal(target.ref.sessionId, "archived"); restored = true;
    return { committed: true, generation: 2, items: [{ target, ref: target.ref, committed: true, workspaceId: "hidden" }] };
  },
});
const opened: string[] = [];
const root = createRoot(document.getElementById("root")!);
const render = (active = true) => <LocaleProvider><ToastProvider><ArchivedSessionsList active={active} onOpenSession={async ref => { opened.push(ref.sessionId); }} /></ToastProvider></LocaleProvider>;
await act(async () => root.render(render()));
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 1, "archives beyond the first page stay reachable in hidden projects");
assert.ok(calls.includes("page-2"));
assert.ok(document.body.textContent?.includes("Hidden project"));
await act(async () => (document.querySelector(".archived-sessions__open") as HTMLButtonElement).click());
assert.deepEqual(opened, [], "preview does not open a writable runtime");
assert.ok(document.body.textContent?.includes("Read-only history"));
await act(async () => (document.querySelector('.archived-sessions__preview [aria-label="Restore session"]') as HTMLButtonElement).click());
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 0);
assert.ok(document.body.textContent?.includes("No archived sessions"));
late = true;
await act(async () => (document.querySelector('.archived-sessions__management') as HTMLButtonElement).click());
await act(async () => (document.querySelector('[role="menuitem"]') as HTMLButtonElement).click());
assert.ok(resolveLate, "refresh starts a pending read");
await act(async () => root.render(render(false)));
await act(async () => resolveLate!({ items: [row("stale")], generation: 2 }));
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 0, "hidden page rejects stale reads");
await act(async () => root.unmount());
host.uninstall();
dom.window.close();
console.log("PASS archived pagination, hidden workspace, navigation, restore and stale-load isolation");
