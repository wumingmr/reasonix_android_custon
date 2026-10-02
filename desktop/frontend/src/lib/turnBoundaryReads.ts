// The per-tab reads that index a session's persisted turns: the checkpoints its
// rewind menu offers and the boundaries its fork entries offer. A turn that
// commits, and a transcript that is replaced, move both, so the two are read
// together where a surface settles; each read keeps its own stale-response
// guard, because one answering late must not overwrite a newer answer.

import { asArray } from "./array";
import { app } from "./bridge";
import { createForkTargetsRefresh, type ForkTurnAction } from "./forkTurn";
import type { CheckpointMeta } from "./types";

/** The actions these reads dispatch into a tab's state. */
export type TurnBoundaryAction = { type: "checkpoints"; checkpoints: CheckpointMeta[] } | ForkTurnAction;

/** The turn-index reads one controller binds, dispatch included. */
export interface TurnBoundaryReads {
  /** Drops in-flight checkpoint and fork reads for a replaced session binding. */
  invalidateCheckpoints(tabId: string): void;
  /** Re-reads a tab's checkpoints alone, for a reconcile that patches only its active turn. */
  refreshCheckpoints(tabId: string): Promise<void>;
  /** Re-reads the checkpoints and the fork boundaries a settled turn changes. */
  refreshTurnBoundaries(tabId: string): Promise<void>;
  /**
   * Applies the checkpoints one hydration read returned and reads the fork
   * boundaries that settle with them; undefined checkpoints dispatch nothing.
   */
  settleCheckpoints(tabId: string, checkpoints: CheckpointMeta[] | undefined): Promise<void>;
}

/** Binds the turn-index reads to one dispatch sink. */
export function createTurnBoundaryReads(dispatch: (tabId: string, action: TurnBoundaryAction) => void): TurnBoundaryReads {
  const checkpointSeq = new Map<string, number>();
  const bumpCheckpoints = (tabId: string): number => {
    const seq = (checkpointSeq.get(tabId) ?? 0) + 1;
    checkpointSeq.set(tabId, seq);
    return seq;
  };
  const refreshCheckpoints = async (tabId: string): Promise<void> => {
    const seq = bumpCheckpoints(tabId);
    const checkpoints = await app.CheckpointsForTab(tabId).catch(() => undefined);
    if (checkpointSeq.get(tabId) !== seq || checkpoints === undefined) return;
    dispatch(tabId, { type: "checkpoints", checkpoints: asArray(checkpoints) });
  };
  const forkTargets = createForkTargetsRefresh(dispatch);
  return {
    invalidateCheckpoints: (tabId) => {
      bumpCheckpoints(tabId);
      forkTargets.invalidate(tabId);
    },
    refreshCheckpoints,
    refreshTurnBoundaries: (tabId) => Promise.all([refreshCheckpoints(tabId), forkTargets.refresh(tabId)]).then(() => undefined),
    settleCheckpoints: async (tabId, checkpoints) => {
      if (checkpoints !== undefined) dispatch(tabId, { type: "checkpoints", checkpoints: asArray(checkpoints) });
      await forkTargets.refresh(tabId);
    },
  };
}
