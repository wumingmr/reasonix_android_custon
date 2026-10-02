import React from "react";
import { createRoot } from "react-dom/client";
import { HistoricalImportList } from "../src/components/HistoricalImportList";
import { HistoricalSessionBanners } from "../src/components/SessionTakeoverDialog";
import { LocaleProvider } from "../src/lib/i18n";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import "../src/styles.css";

let status = {
  items: [
    { id: "old-a", title: "Retained conversation A", format: "legacy", status: "available" },
    { id: "old-b", title: "Retained conversation B", format: "canonical", status: "available" },
  ], running: false, paused: false, remaining: 0,
};
let imports = 0;
let branchImports = 0;
const historicalSource = { hostId: "local", sourceKey: "old-a", path: "/fixture/old-a.jsonl" };
const controls: string[] = [];
installDesktopHostStub({
  CheckHistoricalSourceUpdate: async () => ({ sourceKey: "old-a", status: "available", version: "v2", source: historicalSource, retryable: false }),
  PrepareHistoricalSourceVersion: async () => {
    branchImports++;
    return { operationId: "branch-v2", sourceKey: "old-a", status: "preparing", revision: 1, retryable: false };
  },
  GetSessionPreparation: async () => branchImports === 1
    ? { operationId: "branch-v2", sourceKey: "old-a", status: "failed", errorCode: "import_failed", revision: 2, retryable: true }
    : { operationId: "branch-v2", sourceKey: "old-a", status: "ready", target: { hostId: "local", sessionId: "branch-v2" }, revision: 2, retryable: false },
  ListHistoricalSessions: async () => structuredClone(status),
  GetHistoricalImportStatus: async () => structuredClone(status),
  ImportHistoricalSession: async (id: string) => {
    imports++;
    if (id === "old-a" && imports === 1) throw new Error("historical session is in use; retry after closing the other instance");
    status = { ...status, items: status.items.map(item => item.id === id ? { ...item, status: "imported", session: { hostId: "local", sessionId: "imported-a" } } : item) };
    return { session: { hostId: "local", sessionId: "imported-a" }, workspaceId: "global", generation: 2 };
  },
  StartHistoricalImport: async () => { controls.push("start"); status = { ...status, running: true, remaining: 2 }; return structuredClone(status); },
  ControlHistoricalImport: async (action: string) => {
    controls.push(action);
    status = { ...status, paused: action === "pause", running: action !== "cancel", remaining: action === "cancel" ? 0 : status.remaining };
    return structuredClone(status);
  },
});
createRoot(document.getElementById("root")!).render(<LocaleProvider>
  <div data-testid="source-update-banner"><HistoricalSessionBanners
    tab={{ id: "base", scope: "global", workspaceRoot: "", workspaceName: "Global", topicId: "base", topicTitle: "Base", label: "Base", ready: true, running: false, sessionId: "base" }}
    navigate={async intent => { if (intent.kind === "canonical-session") document.body.dataset.openedBranch = intent.ref.sessionId; }}
  /></div>
  <HistoricalImportList active onOpenSession={async () => {}} />
</LocaleProvider>);
window.addEventListener("beforeunload", () => { document.body.dataset.fixture = JSON.stringify({ controls, imports }); });
