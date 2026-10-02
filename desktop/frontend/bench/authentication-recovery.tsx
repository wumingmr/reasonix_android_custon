// Isolated browser fixture: real Composer, disposable host and no model calls.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { Composer } from "../src/components/Composer";
import { LocaleProvider } from "../src/lib/i18n";
import { ToastProvider } from "../src/lib/toast";
import { installDesktopHostStub } from "../src/__tests__/desktopHostStub";
import type { TabMeta } from "../src/lib/types";
import { modelSettingsAllowSubmission } from "../src/lib/authenticationTypes";
import "../src/styles.css";

let completeRetry: (() => void) | undefined;
installDesktopHostStub({
  Commands: async () => [], Models: async () => [], ModelsForTab: async () => [],
  RetryAuthenticationForTab: async () => {
    await new Promise<void>((resolve) => { completeRetry = resolve; });
    return { status: "ready" };
  },
});

function Fixture() {
  const [tabId, setTabId] = useState("first");
  const [ready, setReady] = useState(true);
  const [authentication, setAuthentication] = useState<NonNullable<TabMeta["authentication"]>>({ status: "authentication_rejected", providerName: "fixture" });
  const [sent, setSent] = useState(0);
  const [modelSettingsPending, setModelSettingsPending] = useState(false);
  return <main style={{ padding: 24 }}>
    <h1>Authentication recovery regression</h1>
    <nav aria-label="Fixture controls">
      <button onClick={() => setTabId(tabId === "first" ? "second" : "first")}>Switch tab</button>
      <button onClick={() => { setReady(false); completeRetry?.(); }}>Finish retry while controller unavailable</button>
      <button onClick={() => { setReady(true); setAuthentication({ status: "missing_credential", providerName: "replacement" }); }}>Replace connection</button>
      <button onClick={() => { setReady(true); setAuthentication({ status: "ready" }); }}>Publish backend ready</button>
      <button onClick={() => { setReady(true); setAuthentication({ status: "missing_credential", providerName: "fixture" }); setModelSettingsPending(false); }}>Missing credential</button>
      <button onClick={() => setModelSettingsPending(true)}>Publish saved settings</button>
    </nav>
    <p>Tab: {tabId}; controller ready: {String(ready)}; model submissions: {sent}</p>
    <div className="chat-pane">
      <Composer running={false} collaborationMode="normal" toolApprovalMode="ask" goal=""
        cwd="/fixture" modelLabel="Fixture model" tabId={tabId} sessionKey={`fixture:${tabId}`}
        ready={ready} authentication={authentication} submitDisabled={!modelSettingsAllowSubmission(ready, { authentication, modelSettingsPending })}
        onSend={() => setSent(n => n + 1)} onCancel={async () => ({ discardedItemIds: [] })}
        onCycleMode={() => {}} onSetMode={() => {}} onSetCollaborationMode={() => {}}
        onSetToolApprovalMode={() => {}} onClearGoal={() => {}} onSwitchModel={() => {}} onSetEffort={() => {}} />
    </div>
  </main>;
}

createRoot(document.getElementById("root")!).render(<LocaleProvider><ToastProvider><Fixture /></ToastProvider></LocaleProvider>);
