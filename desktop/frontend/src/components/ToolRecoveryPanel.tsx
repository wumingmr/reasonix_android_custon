import { ErrorMessage } from "./ErrorMessage";
import { useEffect, useRef, useState } from "react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import type { ToolRecoveryBindings, ToolRecoverySnapshot } from "../lib/toolRecovery";
import "./ToolRecoveryPanel.css";

export function ToolRecoveryPanel({ tabId, sessionKey, running, refreshKey, bindings = app }: {
  tabId: string; sessionKey: string; running: boolean; refreshKey: number;
  bindings?: ToolRecoveryBindings;
}) {
  const t = useT();
  const generation = useRef(0);
  const [snapshot, setSnapshot] = useState<ToolRecoverySnapshot | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const own = ++generation.current;
    setSnapshot(null); setError("");
    if (!running && bindings.GetToolRecoveryForTab) {
      void bindings.GetToolRecoveryForTab(tabId).then(next => {
        if (generation.current === own) setSnapshot(next);
      }).catch(err => { if (generation.current === own) setError(String(err)); });
    }
    return () => { generation.current++; };
  }, [bindings, tabId, sessionKey, running, refreshKey]);

  if (!snapshot?.calls.length && !error) return null;
  return <section className="notice-line notice-line--warn tool-recovery-panel" aria-label={t("toolRecovery.title")}>
    <details open>
    <summary className="notice-line__title">{t("toolRecovery.title")}</summary>
    <div className="notice-line__text">
      {error && <p role="alert"><ErrorMessage error={error} /></p>}
      {!!snapshot?.calls.length && <p>{t("toolRecovery.retired")}</p>}
      {(snapshot?.calls ?? []).map(call => <div key={call.identity.attempt_id}>
        <p>{call.identity.canonical_tool} · {t("toolRecovery.unknown")}</p>
        <details><summary>{t("toolRecovery.details")}</summary>
          <p>{call.identity.resource_scope}</p>
          <p>{call.identity.argument_digest}</p>
          {call.arguments !== undefined && <pre>{JSON.stringify(call.arguments, null, 2)}</pre>}
        </details>
      </div>)}
    </div>
    </details>
  </section>;
}
