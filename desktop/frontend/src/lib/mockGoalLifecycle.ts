import type { TabMeta } from "./types";

export function editMockGoalTab(tab: TabMeta, tabId: string, objective: string, maxGoalRounds: number | null): TabMeta {
  if (tab.id !== tabId) return tab;
  const current = tab.goalView;
  return {
    ...tab,
    goal: objective.trim(),
    goalView: current ? { ...current, objective: objective.trim(), maxGoalRounds, revision: current.revision + 1 } : current,
  };
}
