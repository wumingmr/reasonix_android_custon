import type { SessionDraftSurface } from "../app-runtime/useSessionDraftSurface";

const DRAFT_ATTENTION_PHASES = new Set([
  "accepted",
  "dispatch_unknown",
  "dispatching_shell",
  "resume_required",
  "runtime_failed",
  "terminal_failed",
]);

export function sameDraftError(left: string, right: string | undefined): boolean {
  const normalize = (value: string) => value.trim().replace(/^Error:\s*/, "");
  return Boolean(right) && normalize(left) === normalize(right!);
}

/** Healthy drafts use the new-session landing; recovery controls need room. */
export function draftSurfaceNeedsAttention(draft: SessionDraftSurface): boolean {
  return draft.saveState === "conflict" || draft.saveState === "error" || Boolean(draft.taskError) || Boolean(draft.submissionError)
    || Boolean(draft.operation && DRAFT_ATTENTION_PHASES.has(draft.operation.phase));
}

export function creationHeroVisible(draft: SessionDraftSurface | null | undefined, emptyHero: boolean): boolean {
  // A hidden formal tab must not collapse the active draft's recovery surface.
  return draft ? !draftSurfaceNeedsAttention(draft) : emptyHero;
}
