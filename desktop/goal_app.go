package main

import (
	"strings"

	"reasonix/internal/control"
)

func (a *App) SetGoal(goal string) error { return a.SetGoalForTab("", goal) }

// SetGoalForTab activates or clears a Goal only after the session accepts it.
func (a *App) SetGoalForTab(tabID, goal string) error {
	tab := a.tabByID(tabID)
	if tab == nil {
		return a.workspaceNotReadyErr(nil)
	}
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	goal = strings.TrimSpace(goal)
	approvalMode := a.tabRuntimeSnapshot(tab).currentToolApprovalMode()
	a.mu.Lock()
	if a.tabs[tab.ID] != tab {
		a.mu.Unlock()
		return a.workspaceNotReadyErr(nil)
	}
	ctrl := tab.Ctrl
	plan := tabModeHasPlan(tab.mode) && goal == ""
	a.mu.Unlock()
	if ctrl != nil {
		if err := syncTabGoalToController(ctrl, goal); err != nil {
			return err
		}
		ctrl.SetPlanMode(plan)
	}
	a.mu.Lock()
	if a.tabs[tab.ID] == tab {
		tab.goal = goal
		if goal != "" {
			tab.mode = tabModeFromAxes(false, approvalMode == control.ToolApprovalYolo)
		}
		a.saveTabsLocked()
	}
	a.mu.Unlock()
	return nil
}

// Composer re-sync is idempotent for an already running Goal, while re-entering
// the same text after a terminal state starts a new lifecycle.
func syncTabGoalToController(ctrl control.SessionAPI, goal string) error {
	if ctrl == nil {
		return nil
	}
	goal = strings.TrimSpace(goal)
	if goal != "" && strings.TrimSpace(ctrl.Goal()) == goal && ctrl.GoalStatus() == control.GoalStatusRunning {
		return nil
	}
	return ctrl.SetGoalDurable(goal)
}

func (a *App) ClearGoalForTab(tabID string) error { return a.SetGoalForTab(tabID, "") }

// EditGoalForTab preserves lifecycle identity and admitted rounds. A nil limit
// means unlimited rounds.
func (a *App) EditGoalForTab(tabID, objective string, maxGoalRounds *uint64) error {
	tab := a.tabByID(tabID)
	if tab == nil {
		return a.workspaceNotReadyErr(nil)
	}
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	objective = strings.TrimSpace(objective)
	a.mu.Lock()
	if a.tabs[tab.ID] != tab {
		a.mu.Unlock()
		return a.workspaceNotReadyErr(nil)
	}
	ctrl := tab.Ctrl
	a.mu.Unlock()
	if ctrl == nil {
		return a.workspaceNotReadyErr(nil)
	}
	if err := ctrl.EditGoalDurable(objective, maxGoalRounds); err != nil {
		return err
	}
	a.mu.Lock()
	if a.tabs[tab.ID] == tab {
		tab.goal = objective
		a.saveTabsLocked()
	}
	a.mu.Unlock()
	return nil
}

func (a *App) ResumeGoalForTab(tabID string) bool {
	tab := a.tabByID(tabID)
	if tab == nil {
		return false
	}
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	ctrl := a.controllerForTab(tab)
	if ctrl == nil || !ctrl.ResumeGoal() {
		return false
	}
	a.mu.Lock()
	if a.tabs[tab.ID] == tab {
		tab.goal = strings.TrimSpace(ctrl.Goal())
		a.saveTabsLocked()
	}
	a.mu.Unlock()
	return true
}

func (a *App) PauseGoalForTab(tabID string) bool {
	tab := a.tabByID(tabID)
	if tab == nil {
		return false
	}
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	ctrl := a.controllerForTab(tab)
	return ctrl != nil && ctrl.PauseGoal()
}
