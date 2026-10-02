import assert from "node:assert/strict";
import React, { act } from "react";
import { createRoot } from "react-dom/client";
import { JSDOM } from "jsdom";
import { useSessionExportCommands, type SessionExportFormat } from "../app-runtime/useSessionExportCommands";
import { t } from "../lib/i18n";
import { installDesktopHostStub } from "./desktopHostStub";

const dom = new JSDOM("<!doctype html><div id='root'></div>", { url: "http://localhost", pretendToBeVisual: true });
Object.assign(globalThis, {
  window: dom.window,
  document: dom.window.document,
  Element: dom.window.Element,
  IS_REACT_ACT_ENVIRONMENT: true,
});

const hostExports: Array<{ tabId: string; format: string; title: string }> = [];
const finishes: string[] = [];
const stub = installDesktopHostStub({
  BeginSessionExportForTarget: async (_selector: unknown, tabId: string, format: string, title: string) => {
    hostExports.push({ tabId, format, title });
    return { exportId: `export-${hostExports.length}`, format, snapshot: { title } };
  },
  FinishSessionExport: async (id: string) => { finishes.push(id); return { paths: ["/tmp/full.md"], records: 137, pages: 0 }; },
  CancelSessionExport: async () => {},
  SaveExportFile: async () => { throw new Error("resident exports are forbidden"); },
});

let exportSession!: (format: SessionExportFormat) => Promise<void> | undefined;
function Probe({ remote }: { remote: boolean }) {
  exportSession = useSessionExportCommands({
    tabId: "tab-export",
    remote,
    sessionTitle: "Bounded session",
    items: [{ kind: "user", id: "u1", text: "resident projection" }],
    live: undefined,
    hasContent: true,
    t,
    showToast: () => {},
  }).exportSession;
  return null;
}

const root = createRoot(document.getElementById("root")!);
try {
  await act(async () => root.render(<Probe remote={false} />));
  await act(async () => { await exportSession("markdown"); });
  assert.deepEqual(hostExports, [{ tabId: "tab-export", format: "markdown", title: "Bounded session" }]);
  assert.deepEqual(finishes, ["export-1"]);
  await act(async () => root.render(<Probe remote />));
  await act(async () => { await exportSession("markdown"); });
  assert.equal(hostExports.length, 2, "remote export uses the same authoritative host API");
  assert.deepEqual(finishes, ["export-1", "export-2"]);
  console.log("session export routing: both sources use fixed host snapshots");
} finally {
  await act(async () => root.unmount());
  stub.uninstall();
  dom.window.close();
}
