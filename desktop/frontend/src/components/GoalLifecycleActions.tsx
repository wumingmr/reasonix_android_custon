import { useEffect, useRef, useState } from "react";
import { useI18n } from "../lib/i18n";
import type { GoalLifecycleView } from "../lib/types";

export function GoalLifecycleActions({
  goalView,
  goalStatus,
  disabled,
  running,
  onEditGoal,
  onPauseGoal,
  onResumeGoal,
  onStopGoal,
}: {
  goalView?: GoalLifecycleView;
  goalStatus?: string;
  disabled?: boolean;
  running: boolean;
  onEditGoal: (objective: string, maxGoalRounds: number | null) => void;
  onPauseGoal: () => void;
  onResumeGoal: () => void;
  onStopGoal: () => void;
}) {
  const { t } = useI18n();
  const resumable = goalView?.phase === "paused"
    || goalView?.phase === "blocked"
    || (goalView?.phase === "active" && goalView.activation === "disarmed")
    || (!goalView && goalStatus === "blocked");

  const [draft, setDraft] = useState<{ objective: string; rounds: string; invalid: boolean } | null>(null);

  const editButtonRef = useRef<HTMLButtonElement>(null);
  const wasOpen = useRef(false);
  useEffect(() => {
    if (wasOpen.current && !draft) editButtonRef.current?.focus();
    wasOpen.current = draft !== null;
  }, [draft]);

  const openEditor = () => {
    if (!goalView) return;
    setDraft({ objective: goalView.objective, rounds: goalView.maxGoalRounds?.toString() ?? "", invalid: false });
  };

  const submitEdit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!draft) return;
    const trimmed = draft.rounds.trim();
    const limit = trimmed === "" ? null : Number(trimmed);
    if (limit !== null && (!Number.isSafeInteger(limit) || limit <= 0)) {
      setDraft({ ...draft, invalid: true });
      return;
    }
    setDraft(null);
    onEditGoal(draft.objective, limit);
  };

  return <>
    {draft && (
      <form className="goal-edit-form" data-goal-edit-form="" onSubmit={submitEdit} onKeyDown={(e) => { if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); setDraft(null); } }}>
        <label>
          {t("composer.goalEditObjective")}
          <textarea
            className="remote-secret-dialog__input"
            rows={2}
            data-goal-edit-objective=""
            autoFocus
            value={draft.objective}
            onChange={(e) => setDraft({ ...draft, objective: e.target.value })}
          />
        </label>
        <label>
          {t("composer.goalEditMaxRounds")}
          <input
            className="remote-secret-dialog__input"
            data-goal-edit-rounds=""
            inputMode="numeric"
            value={draft.rounds}
            onChange={(e) => setDraft({ ...draft, rounds: e.target.value, invalid: false })}
          />
        </label>
        {draft.invalid && <p className="goal-edit-form__error" role="alert" data-goal-edit-error="">{t("composer.goalEditInvalidRounds")}</p>}
        <div className="goal-edit-form__actions">
          <button type="button" className="btn" onClick={() => setDraft(null)}>{t("common.cancel")}</button>
          <button type="submit" className="btn btn--primary" disabled={draft.objective.trim() === ""}>{t("common.save")}</button>
        </div>
      </form>
    )}
    {goalView && <button ref={editButtonRef} type="button" className="composer-intent-menu__stop" onClick={openEditor} disabled={disabled || draft !== null}>
      {t("composer.taskModeEditGoal")}
    </button>}
    {resumable ? (
      <button type="button" className="composer-intent-menu__stop" onClick={onResumeGoal} disabled={disabled}>
        {t("composer.taskModeResumeGoal")}
      </button>
    ) : (
      <button type="button" className="composer-intent-menu__stop" onClick={onPauseGoal} disabled={disabled}>
        {t("composer.taskModePauseGoal")}
      </button>
    )}
    <button type="button" className="composer-intent-menu__stop" onClick={onStopGoal} disabled={disabled || running}>
      {t("composer.taskModeStopGoal")}
    </button>
  </>;
}
