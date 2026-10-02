import type { HistoryWindowRequestView } from "./types";
import type { Action, State } from "./useController";
import { getTranscriptStore } from "./transcriptStore";
import { hydrateIdentityCurrent } from "./sessionIdentity";

export type TurnNavigationTarget = { messageId: string; generation?: string; snapshotSequence?: number };
export type TurnNavigationOutcome = "loaded" | "cancelled" | "stale" | "unavailable";
export type NavigateToTurn = (target: TurnNavigationTarget, current: () => boolean) => Promise<TurnNavigationOutcome>;

/** Shared state boundary for local and remote owners; the view never dispatches. */
export async function navigateHistoryTurn(tabId: string, state: State, getState: () => State | undefined,
  dispatch: (action: Action) => void, target: TurnNavigationTarget, current: () => boolean): Promise<TurnNavigationOutcome> {
  const identity = state.meta;
  const valid = () => current() && getState()?.sessionGen === state.sessionGen && hydrateIdentityCurrent(identity ?? {}, getState()?.meta);
  const request: HistoryWindowRequestView = { ...target, anchor: "message", direction: "newer", limit: 32 };
  const result = await getTranscriptStore().prepareTargetWindow(tabId, state.meta?.sessionPath ?? "", request, valid);
  if (!valid() || result.status === "cancelled") return "cancelled";
  if (result.status !== "loaded") return result.status;
  if (!result.current()) return "cancelled";
  result.prepared.commit();
  const projection = result.prepared.projection;
  dispatch({ type: "transcript_records", projection: { ...projection, removeIds: [] }, confirmedUsers: [] });
  return "loaded";
}
