import { useCallback } from "react";
import { app } from "./bridge";

export function useGoalControllerActions(
  activeTabId: string | undefined,
  refreshMetaForTab: (tabId: string) => Promise<unknown>,
) {
  const setGoalForTab = useCallback(async (tabId: string, goal: string): Promise<void> => {
    if (!tabId) return;
    // Propagate activation failures so a structured first Goal turn aborts
    // instead of executing without an active persisted lifecycle.
    try {
      await app.SetGoalForTab(tabId, goal);
    } finally {
      await refreshMetaForTab(tabId);
    }
  }, [refreshMetaForTab]);

  const setGoal = useCallback(async (goal: string): Promise<void> => {
    if (activeTabId) await setGoalForTab(activeTabId, goal);
  }, [activeTabId, setGoalForTab]);

  const editGoalForTab = useCallback(async (tabId: string, objective: string, maxGoalRounds: number | null): Promise<void> => {
    if (!tabId) return;
    try {
      await app.EditGoalForTab(tabId, objective, maxGoalRounds);
    } finally {
      await refreshMetaForTab(tabId);
    }
  }, [refreshMetaForTab]);

  const clearGoalForTab = useCallback(async (tabId: string): Promise<void> => {
    if (!tabId) return;
    try {
      await app.ClearGoalForTab(tabId);
    } finally {
      await refreshMetaForTab(tabId);
    }
  }, [refreshMetaForTab]);

  const clearGoal = useCallback(async (): Promise<void> => {
    if (activeTabId) await clearGoalForTab(activeTabId);
  }, [activeTabId, clearGoalForTab]);

  const resumeGoalForTab = useCallback(async (tabId: string): Promise<boolean> => {
    if (!tabId) return false;
    try {
      const resumed = await app.ResumeGoalForTab(tabId);
      await refreshMetaForTab(tabId);
      return resumed;
    } catch {
      return false;
    }
  }, [refreshMetaForTab]);

  const resumeGoal = useCallback(async (): Promise<boolean> => {
    return activeTabId ? resumeGoalForTab(activeTabId) : false;
  }, [activeTabId, resumeGoalForTab]);

  const pauseGoalForTab = useCallback(async (tabId: string): Promise<boolean> => {
    if (!tabId) return false;
    try {
      const paused = await app.PauseGoalForTab(tabId);
      await refreshMetaForTab(tabId);
      return paused;
    } catch {
      return false;
    }
  }, [refreshMetaForTab]);

  const pauseGoal = useCallback(async (): Promise<boolean> => {
    return activeTabId ? pauseGoalForTab(activeTabId) : false;
  }, [activeTabId, pauseGoalForTab]);

  return { setGoalForTab, setGoal, editGoalForTab, clearGoalForTab, clearGoal, resumeGoalForTab, resumeGoal, pauseGoalForTab, pauseGoal };
}
