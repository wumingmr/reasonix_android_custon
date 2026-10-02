import { useT } from "../lib/i18n";
import type { WireApproval } from "../lib/types";
import { PromptShelf } from "./PromptShelf";

// Old Auto Guard approvals are historical facts. They remain readable without
// confirmation, retry, grant, or replay controls.
export function RetiredRecoveryApproval({ approval }: { approval: WireApproval }) {
  const t = useT();
  const recovery = approval.recovery;
  const action = recovery?.next_action || recovery?.next_tool || approval.subject;
  const detail = recovery?.failed_summary || recovery?.diagnosis || recovery?.change_rationale || recovery?.review_rationale;
  return (
    <PromptShelf
      className="prompt-shelf--recovery-history"
      titleId="recovery-history-title"
      title={t("toolRecovery.historicalTitle")}
      role="region"
    >
      <section className="recovery-summary" aria-label={t("approval.recoverySummaryLabel")}>
        <p className="recovery-summary__reason">{t("toolRecovery.retired")}</p>
        {action && (
          <p className="recovery-summary__action">
            <span>{t("approval.recoveryNextLabel")}</span>
            <code>{action}</code>
          </p>
        )}
        {detail && <p className="recovery-summary__reason">{detail}</p>}
      </section>
    </PromptShelf>
  );
}
