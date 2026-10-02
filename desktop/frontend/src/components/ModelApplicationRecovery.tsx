import { ErrorMessage } from "./ErrorMessage";
import { useEffect, useState } from "react";
import { app } from "../lib/bridge";
import { useT } from "../lib/i18n";
import { appliedOnce, type ModelApplicationChoice, type ModelApplicationDetails } from "../lib/modelApplication";
import { useModelApplicationStatus } from "../lib/useModelApplicationStatus";

type RemoteApplication = { key: string; text: string; details: ModelApplicationDetails };
export function ComposerModelApplicationRecovery({ tabId, local, remote, setRemote, draftKey, text, running, onUseApplied }: {
  tabId?: string;
  local: { application?: ModelApplicationDetails; setApplication(value: ModelApplicationDetails | undefined): void; blocked: boolean };
  remote?: RemoteApplication;
  setRemote(value: RemoteApplication | undefined): void;
  draftKey: string; text: string; running: boolean;
  onUseApplied(choice: ModelApplicationChoice): void;
}) {
  const currentRemote = remote?.key === draftKey && remote.text === text ? remote : undefined;
  const details = local.application ?? currentRemote?.details;
  if (!tabId || !details) return null;
  const change = local.application ? local.setApplication : (value: ModelApplicationDetails | undefined) => setRemote(value && currentRemote ? { ...currentRemote, details: value } : undefined);
  return <ModelApplicationRecovery key={details.runtimeIdentity + details.desiredRevision} tabId={tabId} details={details} onChange={change} blocked={local.blocked || running} onUseApplied={onUseApplied} />;
}

export function ModelApplicationRecovery({ tabId, details, onChange, onUseApplied, blocked }: {
  tabId: string;
  details: ModelApplicationDetails;
  onChange: (value: ModelApplicationDetails | undefined) => void;
  onUseApplied: (choice: ModelApplicationChoice) => void;
  blocked: boolean;
}) {
  const status = useModelApplicationStatus(tabId);
  useEffect(() => {
    if (status?.application === "applied") onChange(undefined);
  }, [status, onChange]);
  const t = useT();
  const [busy, setBusy] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  const [error, setError] = useState("");
  const applying = busy || details.applying;
  const allows = (action: string) => !details.availableActions || details.availableActions.includes(action);
  const retry = async (stop = false) => {
    setBusy(true);
    setError("");
    try {
      if (stop) await app.CancelModelApplicationBlockers?.(tabId, appliedOnce(details), selected);
      const result = await app.RetryModelSettingsApplication(tabId);
      const target = result.targets.find(target => target.tabId === tabId);
      if (target?.application === "applied") onChange(undefined);
      else if (target?.details) onChange(target.details);
      else setError(t("modelApply.failed"));
    } catch {
      setError(t("modelApply.failed"));
    } finally {
      setBusy(false);
    }
  };
  return <div role="alert" className="model-application-recovery">
    <span>{t(details.code === "model_settings_pending" ? "modelApply.pending" : "modelApply.failed", { n: details.blockingJobs.length })}</span>
    <span>{t("modelApply.current", { model: details.model })}</span>
    {details.connectionTarget && <span>{details.connectionTarget}</span>}
    {details.blockingJobs.length > 0 && <button type="button" onClick={() => setExpanded(!expanded)}>{t("modelApply.jobs")}</button>}
    {allows("retry") && <button type="button" disabled={applying} onClick={() => void retry()}>{t("modelApply.retry")}</button>}
    {details.canUseApplied && allows("applied_once") && app.StartTurnWithModelApplication &&
      <button type="button" disabled={blocked || applying} onClick={() => onUseApplied(appliedOnce(details))}>{t("modelApply.useCurrent")}</button>}
    {expanded && <div className="model-application-recovery__jobs">
      {details.blockingJobs.map(job => <label key={job.id}>
        <input type="checkbox" checked={selected.includes(job.id)} onChange={event => setSelected(event.target.checked ? [...selected, job.id] : selected.filter(id => id !== job.id))} />
        {job.label || job.id}
      </label>)}
      {app.CancelModelApplicationBlockers && allows("stop_selected") &&
        <button type="button" disabled={applying || selected.length === 0} onClick={() => void retry(true)}>{t("modelApply.stopSelected")}</button>}
    </div>}
    {!details.canUseApplied && details.continuationUnavailable &&
      <details><summary>{t("modelApply.details")}</summary>{details.continuationUnavailable}</details>}
    {error && <span><ErrorMessage error={error} /></span>}
  </div>;
}
