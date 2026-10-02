package main

import (
	"reasonix/internal/control"
)

// CancelModelApplicationBlockers is a fenced recovery operation, not a generic
// kill-all command. The subsequent apply still waits for actual task exit.
func (a *App) CancelModelApplicationBlockers(tabID string, choice control.ModelApplicationChoice, ids []string) error {
	if a.isRemoteTab(tabID) {
		if err := a.validateRemoteModelConfirmation(tabID, choice); err != nil {
			return err
		}
		return a.remoteTabPost(tabID, "/model-settings/cancel-blockers", map[string]any{"choice": choice, "ids": ids})
	}
	a.runtimeRebuildMu.Lock()
	defer a.runtimeRebuildMu.Unlock()
	a.mu.RLock()
	tab := a.tabByEventSinkIDLocked(tabID)
	a.mu.RUnlock()
	if tab == nil {
		return control.ErrModelChoiceStale
	}
	if a.tabIsReadOnly(tab) {
		return readOnlyChannelErr()
	}
	tab.turnStartMu.Lock()
	defer tab.turnStartMu.Unlock()
	c, ok := a.controllerForTab(tab).(*control.Controller)
	if !ok {
		return control.ErrModelChoiceStale
	}
	if err := c.ValidateModelApplicationIdentity(choice); err != nil {
		return newModelApplicationError(c, err)
	}
	c.CancelModelApplicationBlockers(ids)
	return nil
}
