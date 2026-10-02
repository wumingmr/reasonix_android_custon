import { createRoot } from "react-dom/client";
import { useState } from "react";
import { WorkspaceTurnVerification } from "../src/components/WorkspaceTurnVerification";
import { LocaleProvider, useI18n } from "../src/lib/i18n";
import { createLegacyRemotePolicyNoticeTracker } from "../src/lib/legacyRemotePolicyNotice";
import type { WireCompletionSummary } from "../src/lib/types";
import "../src/styles.css";

// Isolated browser fixture: real presentation components, no service writes.
const base: WireCompletionSummary = {
  preset: "standard", verdict: "unknown", mutations: 1,
  checks_passed: 0, checks_failed: 0, checks_suppressed: 0,
  review: "", constraint_degraded: false,
  receipt: { assessmentKind: "facts", verdict: "unknown" },
};
const cases: [string, WireCompletionSummary][] = [
  ["No observed tests", base],
  ["Model declaration", { ...base, receipt: { ...base.receipt!, gaps: [{ kind: "declared_unverified", detail: "The model reports that integration tests were not run." }] } }],
  ["Failed command", { ...base, receipt: { ...base.receipt!, verifications: [{ command: "go test ./...", passed: false, exitCode: 1 }] } }],
  ["Check predates edits", { ...base, receipt: { ...base.receipt!, verifications: [{ command: "pnpm test", passed: true, stale: true, exitCode: 0 }] } }],
  ["Historical assessment", { ...base, verdict: "partial", review: "missing", receipt: { verdict: "partial", gaps: [{ kind: "missing_check", detail: "Historical missing verification" }] } }],
];
const warning = createLegacyRemotePolicyNoticeTracker()("fixture", "standard", "evaluator_unavailable")!;
function Fixture() {
  const { t, setPref } = useI18n();
  const [selected, setSelected] = useState(0);
  return <main style={{ maxWidth: 760, margin: "24px auto", padding: 24 }}>
    <h1>Execution facts regression</h1>
    <nav><button onClick={() => setPref("en")}>English</button><button onClick={() => setPref("zh")}>中文</button><button onClick={() => setPref("zh-TW")}>繁體中文</button></nav>
    <p role="status">{t(warning)}</p>
    <nav>{cases.map(([label], index) => <button key={label} onClick={() => setSelected(index)}>{label}</button>)}</nav>
    <article style={{ marginTop: 24 }}><h2>{cases[selected][0]}</h2><WorkspaceTurnVerification summary={cases[selected][1]} /></article>
  </main>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><Fixture /></LocaleProvider>);
