import React from "react";
import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import { ChatPaneRegion } from "../src/app-shell/ChatPaneRegion";
import { LocaleProvider, useT } from "../src/lib/i18n";
import { initialState } from "../src/lib/useController";
import type { SessionDraftSurface } from "../src/app-runtime/useSessionDraftSurface";
import "../src/styles.css";

const noop = () => {};
const failure = "Session runtime could not resolve the configuration path.";
const settings = { model: "fixture/model", mode: "normal", toolApprovalMode: "ask", disabledMcp: {}, mcpOrder: [] };
const surface: SessionDraftSurface = {
  kind: "draft", draft: { id: "draft", workspaceId: "workspace", scope: "project", workspaceRoot: "/fixture", revision: 1, contentJson: "{}", settings, status: "active", updatedAt: 1 },
  content: { text: "Keep my draft", invocations: [], attachments: [], workspaceRefs: [], pastedBlocks: [], openPastedLabels: [], sessionRefs: [], selectedTextRefs: [] },
  settings, commands: [], servers: [], generation: 1, editVersion: 0, pendingTasks: 0, preparingSubmission: false, saveState: "saved",
  submissionError: failure, taskError: `Error: ${failure}`,
  operation: { operationId: "original", requestId: "request", draftId: "draft", submissionId: "submission", phase: "runtime_failed", revision: 1, updatedAt: 1,
    canResume: true, canEdit: false, canCancel: true, canDiscard: false, error: failure },
};
let retries = 0;
function Fixture({ pending }: { pending: boolean }) {
  const t = useT();
  return <div className="app" style={{ height: "100vh", display: "flex", flexDirection: "column" }}><ChatPaneRegion
    transitioning={false} t={t} imDetail={null} remote={undefined}
    commands={{ onPrompt: noop, onFork: noop, onLoadOlderHistory: async () => false, onLoadNewerHistory: async () => false, onSurfacePaintReady: noop }}
    transcript={{ state: { ...initialState, meta: { ready: true, eventChannel: "fixture" } }, items: [], footerHeight: 0,
      transcriptHydrating: false, navigationDataReady: true, readOnly: false, controllerReady: false, hydratePlaceholderActive: false,
      clearContextPending: false, availability: { kind: "ready", source: "history" }, rewind: { stateActive: false, committing: false } }}
    onRetryHistory={async () => {}}
    draft={{ surface: { ...surface, resumingSubmission: pending }, onUseSaved: noop, onKeepLocal: noop, onRetrySave: noop,
      onDismissTaskError: noop, onResume: () => { retries++; }, onOpenSession: noop, onCheckSubmission: noop }}
  /></div>;
}
const root = createRoot(document.getElementById("root")!);
const paintDraftRecovery = (pending = false) => flushSync(() => root.render(<LocaleProvider><Fixture pending={pending} /></LocaleProvider>));
Object.assign(window, { paintDraftRecovery, draftRecoveryRetries: () => retries });
paintDraftRecovery();
