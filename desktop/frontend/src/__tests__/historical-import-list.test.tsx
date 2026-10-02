import assert from "node:assert/strict";
import { managementDom } from "../test-support/managementDom";
import { installDesktopHostStub } from "./desktopHostStub";
const dom = managementDom();
const { default: React, act } = await import("react");
const { createRoot } = await import("react-dom/client");
const { LocaleProvider } = await import("../lib/i18n");
const { HistoricalImportList } = await import("../components/HistoricalImportList");
let imports = 0;
let fail = true;
let status = { items: [{ id: "old", title: "Retained conversation", format: "canonical", status: "available" }], running: false, paused: false, remaining: 0 };
const controls: string[] = [];
const opened: string[] = [];
const host = installDesktopHostStub({
  ListHistoricalSessions: async () => status,
  GetHistoricalImportStatus: async () => status,
  ImportHistoricalSession: async () => {
    imports++;
    if (fail) throw new Error("source in use");
    return { session: { hostId: "local", sessionId: "imported" }, workspaceId: "global", generation: 2 };
  },
  StartHistoricalImport: async (ids: string[]) => { assert.deepEqual(ids, []); controls.push("start"); return status = { ...status, running: true, remaining: 1 }; },
  ControlHistoricalImport: async (action: string) => { controls.push(action); return status = { ...status, paused: action === "pause", running: action !== "cancel" }; },
});
const root = createRoot(document.getElementById("root")!);
await act(async () => root.render(<LocaleProvider><HistoricalImportList active onOpenSession={async ref => { opened.push(ref.sessionId); }} /></LocaleProvider>));
const button = (text: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].find(node => node.textContent?.trim() === text)!;
assert.equal(imports, 0, "listing must not import");
await act(async () => button("Import and open").click());
assert.deepEqual(opened, [], "failed import cannot navigate");
assert.equal(document.querySelectorAll(".archived-sessions__row").length, 1, "blocked original stays visible");
fail = false;
await act(async () => button("Import and open").click());
assert.deepEqual(opened, ["imported"]);
await act(async () => button("Import all").click());
await act(async () => button("Pause after current item").click());
await act(async () => button("Continue").click());
await act(async () => button("Cancel import").click());
assert.deepEqual(controls, ["start", "pause", "resume", "cancel"]);
assert.ok(document.body.textContent?.includes("Original files are kept"));
await act(async () => root.unmount());
host.uninstall(); dom.window.close();
console.log("PASS explicit import, failure retention, open after commit and batch controls");
