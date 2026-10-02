import { useEffect, useRef, useState } from "react";
import { app } from "../lib/bridge";
import { asArray } from "../lib/array";
import { useT } from "../lib/i18n";
import type { ManualSessionCreationView } from "../generated/desktopContract.generated";
import { manualCreationPresentation } from "../lib/manualCreationPresentation";
import type { ManualCreationObservation } from "../lib/manualCreationRequests";

type Props = {
  sessionId?: string;
  attempt?: ManualCreationObservation | null;
  onRetry?: () => Promise<void>;
  onNew?: () => Promise<void>;
  onChooseProject?: () => unknown;
};

/** One notice for the selected surface. Mount with a navigation-scoped key. */
export function ManualSessionRecovery({ sessionId, attempt, onRetry, onNew, onChooseProject }: Props) {
  const t = useT();
  const [restored, setRestored] = useState<ManualSessionCreationView>();
  const [error, setError] = useState(false);
  const [busy, setBusy] = useState(false);
  const [dismissed, setDismissed] = useState(false);
  const inFlight = useRef(false);
  const observation = useRef(0);
  useEffect(() => {
    // Explicit creation already has an observer; never poll it a second time.
    if (attempt || !sessionId || dismissed) return;
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    let id: string | undefined;
    const refresh = async () => {
      const sequence = ++observation.current;
      let again = true;
      try {
        const item = id ? await app.GetManualSessionCreation(id)
          : asArray(await app.ListManualSessionCreations()).find(item => item.ref?.sessionId === sessionId);
        if (disposed || sequence !== observation.current) return;
        setRestored(item); setError(false);
        if (!item || item.phase === "ready") { again = false; return; }
        id = item.operationId;
      } catch {
        if (disposed || sequence !== observation.current) return;
        // A catalog failure is not evidence this particular session failed.
        if (id) setError(true);
      } finally {
        if (!disposed && again) timer = setTimeout(() => void refresh(), 1500);
      }
    };
    void refresh();
    return () => { disposed = true; ++observation.current; clearTimeout(timer); };
  }, [attempt, sessionId, dismissed]);

  const item = attempt?.operation ?? restored;
  const failed = error || attempt?.failed;
  if (dismissed || (!attempt && !item) || (!failed && item?.phase === "ready")) return null;
  const state = failed ? { key: "creation.requestError" as const, action: "retry" as const }
    : item ? manualCreationPresentation(item) : { key: "creation.opening" as const, action: undefined };
  if (state.key === "creation.preparing" && attempt && !item?.surfaceReady) state.key = "creation.opening";
  const run = async (action: () => unknown) => {
    if (inFlight.current) return;
    inFlight.current = true; setBusy(true);
    try { await action(); setError(false); }
    catch { setError(true); }
    finally { inFlight.current = false; setBusy(false); }
  };
  const retry = async () => {
    if (attempt) { await onRetry?.(); return; }
    if (item) {
      const updated = await app.RetryManualSessionCreation(item.operationId);
      ++observation.current;
      setRestored(updated);
    }
  };
  const action = state.action === "retry" ? retry : state.action === "new" ? onNew : state.action === "project" ? onChooseProject : undefined;
  return <div className="creation-notice" data-testid="creation-notice">
    <div className="creation-notice__row">
      <span role="status">{t(state.key)}</span>
      {action && <button className="btn btn--small" disabled={busy || attempt?.pending}
        onClick={() => void run(action)}>{t(state.action === "new" ? "topbar.newSession" : state.action === "project" ? "creation.chooseProject" : "creation.retry")}</button>}
      {state.action && <button className="btn btn--small" disabled={busy}
        onClick={() => setDismissed(true)}>{t("common.close")}</button>}
    </div>
    {state.action && <details>
      <summary>{t("sessionRecovery.details")}</summary>
      <button className="btn btn--small" disabled={busy}
        onClick={() => void run(() => app.ExportManualCreationDiagnostics())}>{t("creation.export")}</button>
    </details>}
  </div>;
}
