import React, { useState } from "react";
import { createRoot } from "react-dom/client";
import { ModelApplicationRecovery } from "../src/components/ModelApplicationRecovery";
import { LocaleProvider } from "../src/lib/i18n";
import type { ModelApplicationDetails } from "../src/lib/modelApplication";
import type { ReasonixDesktopHost } from "../src/lib/desktopHost";
import "../src/styles.css";

const initial: ModelApplicationDetails = { code: "model_settings_pending", runtimeIdentity: "runtime", appliedRevision: "old", desiredRevision: "new", model: "fixture/model", connectionTarget: "http://localhost:9999", blockingJobs: [{ id: "task-1", kind: "task", label: "Dependent task", status: "running" }, { id: "task-2", kind: "task", label: "Another task", status: "running" }], canUseApplied: true };
const fixture = { calls: [] as unknown[][], applied: false, sends: 0 };
const status = () => ({ application: fixture.applied ? "applied" : "pending", targets: [{ tabId: "tab", application: fixture.applied ? "applied" : "pending", details: initial }], issues: [], appliedCatalogs: [] });
const commands: Record<string, (...args: unknown[]) => unknown> = {
  GetModelSettingsApplication: status,
  RetryModelSettingsApplication: () => { fixture.applied = true; return status(); },
  CancelModelApplicationBlockers: (...args) => { fixture.calls.push(args); },
  StartTurnWithModelApplication: () => {},
};
window.reasonixDesktop = { kind: "electron", contract: { commands: Object.keys(commands) }, native: { window: {} }, on: () => () => {}, invoke: async (method, args) => commands[method](...args) } as unknown as ReasonixDesktopHost;
Object.assign(window, { modelApplicationFixture: fixture });

function Fixture() {
  const [details, setDetails] = useState<ModelApplicationDetails | undefined>(initial);
  const [text, setText] = useState("Keep my unsent message and attachment");
  return <main style={{ maxWidth: 800, margin: 32 }}>
    <p>Existing conversation remains here.</p>
    <textarea aria-label="Draft" value={text} onChange={event => setText(event.target.value)} />
    <span data-testid="attachment">fixture.txt</span>
    {details && <ModelApplicationRecovery tabId="tab" details={details} onChange={setDetails} blocked={false} onUseApplied={choice => { fixture.calls.push([choice]); fixture.sends++; }} />}
  </main>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><Fixture /></LocaleProvider>);
