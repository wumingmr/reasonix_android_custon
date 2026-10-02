import { useCallback, type RefObject } from "react";
import type { State, Action } from "./useController";
import type { TurnNavigationTarget } from "./historyTurnNavigation";

export function useHistoryTurnNavigation(states: RefObject<Map<string, State>>, sequences: RefObject<Map<string, number>>,
  dispatch: (tab: string, action: Action) => void) {
  return useCallback(async (tab: string, target: TurnNavigationTarget, current: () => boolean) => {
    const state = states.current.get(tab);
    if (!state) return "cancelled" as const;
    const sequence = (sequences.current.get(tab) ?? 0) + 1;
    sequences.current.set(tab, sequence);
    const { navigateHistoryTurn } = await import("./historyTurnNavigation");
    return navigateHistoryTurn(tab, state, () => states.current.get(tab), action => dispatch(tab, action), target,
      () => current() && sequences.current.get(tab) === sequence);
  }, [states, sequences, dispatch]);
}
