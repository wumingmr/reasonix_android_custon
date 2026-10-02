package main

import "reasonix/internal/control"

// abort releases admission before entering the serialized rebuild transaction.
func (a *App) applyTurnModelSettings(tab *WorkspaceTab, ctrl control.SessionAPI, usingApplied bool, abort func()) (bool, error) {
	if a.ctx == nil || usingApplied {
		return false, nil
	}
	needed, err := modelSettingsNeedApply(ctrl)
	if err != nil {
		abort()
		return false, newModelApplicationError(ctrl, err)
	}
	if !needed {
		return false, nil
	}
	a.mu.RLock()
	pending := tab.PendingCreateOperationID != ""
	a.mu.RUnlock()
	abort()
	if pending {
		return false, control.ErrModelChoiceStale
	}
	if err := a.refreshTabModelSettings(tab); err != nil {
		return false, err
	}
	return true, nil
}
