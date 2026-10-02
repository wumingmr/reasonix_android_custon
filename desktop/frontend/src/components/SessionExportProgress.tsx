import { useSyncExternalStore } from "react";
import { cancelSessionExport, sessionExportProgress } from "../lib/sessionExportProgress";
import { t } from "../lib/i18n";
export function SessionExportProgress() {
  const operations = useSyncExternalStore(sessionExportProgress.subscribe, sessionExportProgress.getSnapshot);
  if (!operations.length) return null;
  return <div className="session-export-progress" role="status" aria-live="polite">
    {operations.map(operation => <div key={operation.exportId}>
      <span>{operation.title} · {t(`topicBar.exportPhase.${operation.phase}` as Parameters<typeof t>[0])} · {operation.pages || operation.records}</span>
      <button type="button" onClick={() => { void cancelSessionExport(operation.exportId).catch(() => {}); }}>{t("topicBar.exportCancel")}</button>
    </div>)}
  </div>;
}
