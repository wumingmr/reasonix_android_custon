import { useT } from "../lib/i18n";
import type { useSessionComposerPersistence } from "../lib/sessionComposerPersistence";

type InputState = ReturnType<typeof useSessionComposerPersistence>;

export function SessionInputRecovery({ input }: { input: InputState }) {
  const t = useT();
  if (!input.error && !input.attention && !input.recovering) return null;
  return <div role="alert" className="session-draft-surface__error">
      <span>{t(input.recovering ? "composer.inputRecovering" : input.attention ? "composer.inputUnconfirmed" : "composer.inputRetry")}</span>
      {!input.recovering && <button type="button" onClick={() => void input.retry().catch(() => {})}>{t("common.retry")}</button>}
    </div>;
}
