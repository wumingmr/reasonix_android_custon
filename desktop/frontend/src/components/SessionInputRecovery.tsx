import { useT } from "../lib/i18n";
import type { useSessionComposerPersistence } from "../lib/sessionComposerPersistence";
import { ErrorMessage } from "./ErrorMessage";

type InputState = ReturnType<typeof useSessionComposerPersistence>;

export function SessionInputRecovery({ input }: { input: InputState }) {
  const t = useT();
  if (!input.error && !input.attention && !input.recovering) {
    if (!input.taskError) return null;
    return <div role="alert" className="session-draft-surface__error">
      <ErrorMessage error={input.taskError} />
      <button type="button" onClick={input.dismissTaskError}>{t("common.close")}</button>
    </div>;
  }
  return <div role="alert" className="session-draft-surface__error">
      <span>{t(input.recovering ? "composer.inputRecovering" : input.attention ? "composer.inputUnconfirmed" : "composer.inputRetry")}</span>
      {!input.recovering && <button type="button" onClick={() => void input.retry().catch(() => {})}>{t("common.retry")}</button>}
    </div>;
}
