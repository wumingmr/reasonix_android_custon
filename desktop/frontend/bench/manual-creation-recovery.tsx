import React from "react";
import { createRoot } from "react-dom/client";
import { ManualSessionRecovery } from "../src/components/ManualSessionRecovery";
import { LocaleProvider } from "../src/lib/i18n";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import "../src/styles.css";

let status = "waiting_lock";
let retries = 0;
let exports = 0;
let release: (() => void) | undefined;
const item = () => ({ operationId: "browser-creation", scope: "global", phase: "starting", ref: { hostId: "local", sessionId: "current" },
  progress: { status, stage: "building_runtime", stageStartedAt: Date.now(), elapsedMs: 0, slow: false } });
installDesktopHostStub({
  ListManualSessionCreations: async () => [item(), { ...item(), operationId: "old", ref: { hostId: "local", sessionId: "other" }, workspaceRoot: "/private/old-project", phase: "failed" }],
  GetManualSessionCreation: async () => item(),
  RetryManualSessionCreation: async () => {
    retries++;
    await new Promise<void>(resolve => { release = resolve; });
    status = "running";
    return item();
  },
  ExportManualCreationDiagnostics: async () => { exports++; return "creation-diagnostics.json"; },
});
Object.assign(window, { creationFixture: { finish: () => release?.(), setStatus: (next: string) => { status = next; }, counts: () => ({ retries, exports }) } });
createRoot(document.getElementById("root")!).render(<LocaleProvider><ManualSessionRecovery sessionId="current" /></LocaleProvider>);
