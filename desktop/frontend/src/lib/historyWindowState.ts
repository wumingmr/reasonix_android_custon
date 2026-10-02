import { compactArchivedToolItems } from "./archivedToolItems";
import { duplicateLiveItemIds } from "./hydrateHistoryApply";
import { historyRevisionIsOlder } from "./sessionTranscriptMode";
import type { HistoryMutation, Item } from "./useController";
import { canonicalUserConfirmations, settleLocalSubmissions, type LocalSubmissionFields } from "./localSubmissionState";
import { reconcileSessionOperationItems } from "./sessionMaintenanceOperation";

type WindowFields = LocalSubmissionFields & {
  items: Item[];
  historyPrefixCount: number;
  hydrateHistoryLoaded?: boolean;
  hydratePlaceholderItems?: Item[];
  historyStartTurn: number;
  historyEndTurn: number;
  historyTotalTurns: number;
  historyHasOlder: boolean;
  historyHasNewer: boolean;
  historyOlderLoading: boolean;
  historyOlderError?: string;
  historyNewerLoading: boolean;
  historyNewerError?: string;
  historyRevision?: number;
  historyDigest?: string;
  historyLayoutRevision: number;
  historyMutation: HistoryMutation;
  pendingSubmissionId?: string;
};

export type HistoryWindowMutationAction = {
  type: "history_replace" | "history_rebase" | "history_prepend" | "history_append";
  items: Item[];
  removeIds?: string[];
  startTurn: number;
  endTurn?: number;
  totalTurns: number;
  hasOlder: boolean;
  hasNewer?: boolean;
  revision?: number;
  digest?: string;
};

function common<S extends WindowFields>(state: S, action: HistoryWindowMutationAction, items: Item[], prefix: number, kind: "replace" | "prepend" | "append"): S {
  return {
    ...state,
    items: compactArchivedToolItems(items),
    historyPrefixCount: prefix,
    hydrateHistoryLoaded: true,
    hydratePlaceholderItems: undefined,
    historyStartTurn: action.startTurn,
    historyEndTurn: action.endTurn ?? (kind === "prepend" ? state.historyEndTurn : action.totalTurns),
    historyTotalTurns: action.totalTurns,
    historyHasOlder: action.hasOlder,
    historyHasNewer: Boolean(action.hasNewer),
    historyOlderLoading: false,
    historyOlderError: undefined,
    historyNewerLoading: false,
    historyNewerError: undefined,
    historyRevision: action.revision,
    historyDigest: action.digest,
    historyMutation: { seq: state.historyMutation.seq + 1, kind },
  };
}

export function reduceHistoryWindowState<S extends WindowFields>(state: S, action: HistoryWindowMutationAction): S {
  if (historyRevisionIsOlder(state.historyRevision, action.revision)) return state;
  if (action.type === "history_replace") {
    const items = reconcileSessionOperationItems(action.items, state.items);
    return settleLocalSubmissions(common(state, action, items, items.length, "replace"), items);
  }
  if (action.type === "history_rebase") {
    const liveTail = state.items.slice(Math.min(state.historyPrefixCount, state.items.length));
    const duplicates = new Set(duplicateLiveItemIds(action.items, liveTail));
    const items = reconcileSessionOperationItems([...action.items, ...liveTail.filter((item) => !duplicates.has(item.id))], state.items);
    return settleLocalSubmissions({ ...common(state, action, items, action.items.length, "replace"), historyLayoutRevision: state.historyLayoutRevision + 1 }, items, canonicalUserConfirmations(action.items));
  }
  if (action.type === "history_prepend") {
    const remove = action.removeIds?.length ? new Set(action.removeIds) : undefined;
    const rest = remove ? state.items.filter((item) => !remove.has(item.id)) : state.items;
    const prefix = state.items.slice(0, Math.min(state.historyPrefixCount, state.items.length));
    const retainedPrefix = remove ? prefix.filter((item) => !remove.has(item.id)) : prefix;
    const incoming = new Set(action.items.map(item => item.id));
    const items = reconcileSessionOperationItems([...new Map(action.items.map(item => [item.id, item])).values(), ...rest.filter(item => !incoming.has(item.id))], state.items);
    return settleLocalSubmissions(common(state, action, items, action.items.length + retainedPrefix.filter(item => !incoming.has(item.id)).length, "prepend"), items, canonicalUserConfirmations(action.items));
  }
  const items = reconcileSessionOperationItems(action.items, state.items);
  return settleLocalSubmissions({
    ...common(state, action, items, items.length, "append"),
    historyLayoutRevision: state.historyLayoutRevision + 1,
  }, items);
}
